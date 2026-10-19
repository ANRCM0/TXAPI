package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

func TestP0TLSHTTP2ProxyStreamingLifecycle(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Responses} {
		for _, downstreamHTTP2 := range []bool{false, true} {
			for _, upstreamHTTP2 := range []bool{false, true} {
				for _, mode := range []string{"terminal_open", "truncated", "cancel", "idle"} {
					name := string(operation) + "/downstream_" + proxyProtocolName(downstreamHTTP2) + "/upstream_" + proxyProtocolName(upstreamHTTP2) + "/" + mode
					t.Run(name, func(t *testing.T) {
						p0ProxyLifecycle(t, operation, downstreamHTTP2, upstreamHTTP2, mode)
					})
				}
			}
		}
	}
}

func proxyProtocolName(http2 bool) string {
	if http2 {
		return "h2"
	}
	return "h1"
}

func p0ProxyLifecycle(t *testing.T, operation protocol.Operation, downstreamHTTP2, upstreamHTTP2 bool, mode string) {
	t.Helper()
	partial, terminal := p0SocketFrames(operation)
	terminalAllowed := make(chan struct{})
	release := make(chan struct{})
	// Deferred before server cleanup so even a failed read releases handlers.
	defer close(release)
	observedProtocol := make(chan int, 4)
	upstreamClosed := make(chan struct{}, 4)
	var hits atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		observedProtocol <- r.ProtoMajor
		defer func() { upstreamClosed <- struct{}{} }()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "proxy-controlled-upstream")
		// SSE lines and UTF-8 runes cross TLS/HTTP2 writes. Receiving a write
		// boundary is deliberately not required of the downstream client.
		for data := []byte(partial); len(data) > 0; {
			length := min(7, len(data))
			if _, err := w.Write(data[:length]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			data = data[length:]
		}
		if mode == "truncated" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-release:
			return
		case <-terminalAllowed:
		}
		if _, err := io.WriteString(w, terminal); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		// Intentionally no EOF after terminal: the gateway must stop observing
		// and cancel/close the upstream stream before this gate is released.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	upstream.EnableHTTP2 = upstreamHTTP2
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	fixture := newP0SocketGatewayWithOptions(t, upstream.URL, operation, func(opts *Options) {
		opts.Client = upstream.Client() // Trust only this fixture's certificate.
		opts.ForwardUpstreamMetadata = true
		if mode == "idle" {
			opts.HTTPStageTimeouts.StreamIdle = 250 * time.Millisecond
		}
	})
	target, err := url.Parse(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Clone instead of mutating DefaultTransport; this hop is explicit HTTP/1.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	proxy.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	edge := httptest.NewUnstartedServer(proxy)
	edge.EnableHTTP2 = downstreamHTTP2
	edge.StartTLS()
	t.Cleanup(edge.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	if operation == protocol.Responses {
		body = `{"model":"m","stream":true,"input":"hello","store":false}`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, edge.URL+"/test", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := edge.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	expectedDownstream, expectedUpstream := 1, 1
	if downstreamHTTP2 {
		expectedDownstream = 2
	}
	if upstreamHTTP2 {
		expectedUpstream = 2
	}
	if response.ProtoMajor != expectedDownstream || response.StatusCode != http.StatusOK ||
		response.Header.Get("X-TXAPI-Upstream-Request-ID") != "proxy-controlled-upstream" {
		t.Fatalf("proxy response: protocol=%s status=%d headers=%v", response.Proto, response.StatusCode, response.Header)
	}
	select {
	case actual := <-observedProtocol:
		if actual != expectedUpstream {
			t.Fatalf("upstream negotiated HTTP/%d, expected HTTP/%d", actual, expectedUpstream)
		}
	case <-ctx.Done():
		t.Fatal("upstream protocol was not observed")
	}
	reader := bufio.NewReader(response.Body)
	var received strings.Builder
	// The normal terminal is still held. Reading one whole business event
	// proves proxy flushing without a timing assertion or an early heartbeat.
	for {
		line, err := reader.ReadString('\n')
		received.WriteString(line)
		if err != nil {
			t.Fatalf("proxy did not deliver a complete text event: %v body=%s", err, received.String())
		}
		if line == "\n" && strings.Contains(received.String(), "你好🌏") {
			break
		}
	}
	if mode == "cancel" {
		cancel()
		response.Body.Close()
	} else {
		if mode == "terminal_open" {
			close(terminalAllowed)
		}
		rest, err := io.ReadAll(reader)
		received.Write(rest)
		if err != nil {
			t.Fatalf("proxy response did not end cleanly: %v body=%s", err, received.String())
		}
	}
	result := fixture.result(t)
	if !result.Committed || hits.Load() != 1 || len(result.Attempts) != 1 {
		t.Fatalf("proxy execution/commit/retry: result=%+v upstream_hits=%d", result, hits.Load())
	}
	select {
	case <-upstreamClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream handler outlived gateway completion/cancellation")
	}
	text := received.String()
	if !utf8.ValidString(text) {
		t.Fatalf("invalid UTF-8 after proxy: %q", text)
	}
	switch mode {
	case "terminal_open":
		if result.StatusCode != 200 || !result.ProtocolComplete || result.Error != nil {
			t.Fatalf("proxy success facts: %+v", result)
		}
		if arguments := p0SocketToolArguments(t, []byte(text), operation); arguments != `{"city":"上海"}` {
			t.Fatalf("proxy changed tool arguments: %q", arguments)
		}
		if operation == protocol.Chat && strings.Count(text, "[DONE]") != 1 {
			t.Fatalf("proxy Chat terminal: %s", text)
		}
		if operation == protocol.Responses && (strings.Count(text, "event: response.completed") != 1 ||
			!strings.Contains(text, "resp_local") || strings.Contains(text, "resp_upstream") || strings.Contains(text, "[DONE]")) {
			t.Fatalf("proxy Responses identity/terminal: %s", text)
		}
	case "truncated", "cancel", "idle":
		if result.ProtocolComplete || result.Error == nil || strings.Contains(text, "[DONE]") || strings.Contains(text, "event: response.completed") {
			t.Fatalf("proxy invented completion: result=%+v body=%s", result, text)
		}
		if mode == "truncated" && result.StatusCode != 502 || mode == "cancel" && result.StatusCode != 499 ||
			mode == "idle" && (result.StatusCode != 504 || result.TimeoutStage != core.TimeoutStreamIdle) {
			t.Fatalf("proxy failure facts (%s): %+v", mode, result)
		}
	}
}
