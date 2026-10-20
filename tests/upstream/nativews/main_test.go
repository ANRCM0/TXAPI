package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestChatFixtureHTTP2TerminalAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	f := &fixture{root: ctx}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
	provider.EnableHTTP2 = true
	provider.StartTLS()
	t.Cleanup(provider.Close)
	t.Cleanup(cancel)
	client := provider.Client()
	t.Cleanup(client.CloseIdleConnections)
	for _, input := range []string{"h2-complete", "h2-cancel", "h2-recovery"} {
		payload := `{"model":"m","stream":true,"messages":[{"role":"user","content":"` + input + `"}]}`
		r, _ := http.NewRequestWithContext(ctx, "POST", provider.URL+"/v1/chat/completions", strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+upstreamKey)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if response.ProtoMajor != 2 || response.StatusCode != 200 {
			t.Fatal(response.Proto, response.StatusCode)
		}
		scanner := bufio.NewScanner(response.Body)
		seen := false
		for scanner.Scan() {
			if input == "h2-cancel" && scanner.Text() == "" || scanner.Text() == "data: [DONE]" {
				seen = true
				break
			}
		}
		_ = response.Body.Close()
		if !seen {
			t.Fatal("fixture stream missing terminal/first event", scanner.Err())
		}
		deadline := time.Now().Add(2 * time.Second)
		for f.chatActive.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.chatActive.Load() != 0 {
			t.Fatal("fixture stream not cancelled")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.chat) != 3 || len(f.errors) != 0 {
		t.Fatal("fixture observations", f.chat, f.errors)
	}
	for _, row := range f.chat {
		if row.Protocol != 2 || row.Peer != f.chat[0].Peer {
			t.Fatal("HTTP/2 reuse", f.chat)
		}
	}
}

func TestChatClientCompleteCancelAndTruncation(t *testing.T) {
	for _, mode := range []string{"h2-complete", "h2-cancel", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var active atomic.Int64
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				active.Add(1)
				defer active.Add(-1)
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("Chat client boundary")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好🌏\"}}]}\n\n")
				_ = http.NewResponseController(w).Flush()
				if mode == "h2-cancel" {
					select {
					case <-r.Context().Done():
					case <-ctx.Done():
					}
					return
				}
				if mode == "truncated" {
					return
				}
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(front.Close)
			t.Cleanup(cancel)
			var report bytes.Buffer
			err := chatCall(ctx, front.Client(), front.URL, "fixture", mode, &report)
			if (err != nil) != (mode == "truncated") {
				t.Fatal("Chat client terminal", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("Chat client did not release stream")
			}
			if err == nil && !json.Valid(report.Bytes()) {
				t.Fatal("invalid fixture report")
			}
		})
	}
}

// This only checks the deployment harness: its CA, protocol fixture, client and
// Upgrade proxy. It is not evidence about the shipped gateway or Docker wiring.
func TestIsolatedFixtureTLSAndProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	directory := t.TempDir()
	if err := prepare(directory); err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(directory, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture CA missing")
	}
	f := &fixture{root: ctx}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
	provider.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	provider.StartTLS()
	t.Cleanup(provider.Close)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != "api.openai.com:443" {
				t.Error("unexpected fixture TLS destination", address)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", provider.Listener.Addr().String())
		}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	requestHTTP, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses",
		strings.NewReader(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"HTTP root"}]}],"store":true}`))
	if err != nil {
		t.Fatal(err)
	}
	requestHTTP.Header.Set("Authorization", "Bearer "+upstreamKey)
	response, err := client.Do(requestHTTP)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("fixture rejected local-history HTTP input", response.StatusCode)
	}
	connection, _, err := websocket.Dial(ctx, "wss://api.openai.com/v1/responses", &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
	if err != nil {
		t.Fatal("fixture signed TLS handshake", err)
	}
	request := `{"type":"response.create","model":"m","input":[{"role":"user","content":"fixture"}],"store":false,"stream_id":"main"}`
	if err := connection.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
		t.Fatal(err)
	}
	terminal := false
	for count := 0; count < 8 && !terminal; count++ {
		_, raw, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct{ Type string }
		if json.Unmarshal(raw, &event) != nil {
			t.Fatal("fixture emitted invalid event")
		}
		terminal = event.Type == "response.completed"
	}
	_ = connection.CloseNow()
	if !terminal {
		t.Fatal("fixture terminal missing")
	}
	for _, status := range []string{"failed", "incomplete"} {
		c, _, err := websocket.Dial(ctx, "wss://api.openai.com/v1/responses", &websocket.DialOptions{HTTPClient: client,
			HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
		if err != nil {
			t.Fatal(err)
		}
		payload := strings.Replace(request, `"fixture"`, `"`+status+`"`, 1)
		if err := c.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		seen := false
		for count := 0; count < 8; count++ {
			_, raw, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var event struct {
				Type     string
				Response struct {
					Status string
					Usage  json.RawMessage
				}
			}
			if json.Unmarshal(raw, &event) != nil || event.Type == "response.completed" {
				t.Fatal("fixture fabricated success", string(raw))
			}
			if event.Type == "response."+status {
				if event.Response.Status != status || string(event.Response.Usage) != "null" {
					t.Fatal("fixture lost non-success/unknown usage", string(raw))
				}
				seen = true
				break
			}
		}
		_ = c.CloseNow()
		if !seen {
			t.Fatal("fixture non-success terminal missing", status)
		}
	}
	// Simulate only the provider wire contract so the fixture client and proxy
	// can be checked on Windows before the real Linux image is available.
	custom, _, err := websocket.Dial(ctx, "wss://api.openai.com/v1/responses", &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := custom.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"m","input":[{"role":"user","content":"custom"}],"store":false,"stream_id":"main","tools":[{"type":"namespace","name":"text","tools":[{"type":"custom","name":"edit"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	var deltas strings.Builder
	for {
		_, raw, err := custom.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type, Delta string
			Response    struct {
				Output []struct{ Type, Input, Namespace, Name string }
			}
		}
		if json.Unmarshal(raw, &event) != nil {
			t.Fatal("invalid custom fixture event")
		}
		if event.Type == "response.custom_tool_call_input.delta" {
			deltas.WriteString(event.Delta)
		}
		if event.Type == "response.completed" {
			if len(event.Response.Output) != 1 || event.Response.Output[0].Type != "custom_tool_call" || event.Response.Output[0].Input != deltas.String() || deltas.String() != "raw 雪🌏\nsecond" || event.Response.Output[0].Namespace != "text" || event.Response.Output[0].Name != "edit" {
				t.Fatal("custom TLS fixture changed native input", string(raw))
			}
			break
		}
	}
	_ = custom.CloseNow()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("X-Forwarded-For")
		f.provider(w, r)
	}))
	t.Cleanup(backend.Close)
	proxy, err := proxyFor(backend.URL, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Transport.(*http.Transport).CloseIdleConnections)
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	var events bytes.Buffer
	if err := wsCall(ctx, front.Client(), front.URL, upstreamKey, request, "terminal", &events); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(events.Bytes(), []byte("你好🌏")) || f.upgrades.Load() != 1 {
		t.Fatal("proxy failed to forward complete fixture events")
	}
	// The certificate must be bound to the fixture's official-address alias.
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || parsed.VerifyHostname("other.invalid") == nil {
		t.Fatal("fixture certificate accepted a different host")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 6 || len(f.errors) != 0 || f.requests[0].InputKind != "array" || !f.requests[0].Store || f.requests[1].Store {
		t.Fatal("fixture observations did not match controlled requests", f.errors)
	}
}
