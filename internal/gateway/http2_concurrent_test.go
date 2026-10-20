package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

func TestP0HTTP2ConcurrentCancellationIsolationAndFullRefill(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Responses} {
		for _, cancelled := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/cancel_%d_of_4", operation, cancelled), func(t *testing.T) {
				p0HTTP2Concurrent(t, operation, cancelled)
			})
		}
	}
}

func p0HTTP2Concurrent(t *testing.T, operation protocol.Operation, cancelled int) {
	t.Helper()
	const slots = 4
	type stream struct {
		id          string
		finish, end chan struct{}
		hits        atomic.Int64
		cancel      context.CancelFunc
		response    *http.Response
		reader      *bufio.Reader
		prefix      string
	}
	streams := make(map[string]*stream)
	for i := 0; i < slots*2+cancelled; i++ {
		id := fmt.Sprintf("stream_%02d", i)
		streams[id] = &stream{id: id, finish: make(chan struct{}), end: make(chan struct{})}
	}
	release := make(chan struct{})
	defer close(release)
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	var upstreamConnections, edgeConnections, hits, active, invalid atomic.Int64
	partial, terminal := p0SocketFrames(operation)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input    string `json:"input"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(request.Body).Decode(&payload) != nil {
			invalid.Add(1)
			w.WriteHeader(400)
			return
		}
		id := payload.Input
		if operation == protocol.Chat && len(payload.Messages) == 1 {
			id = payload.Messages[0].Content
		}
		s := streams[id]
		if s == nil || request.ProtoMajor != 2 {
			invalid.Add(1)
			w.WriteHeader(400)
			return
		}
		hits.Add(1)
		if s.hits.Add(1) != 1 {
			invalid.Add(1)
			w.WriteHeader(500)
			return
		}
		active.Add(1)
		defer func() { active.Add(-1); close(s.end) }()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.ReplaceAll(partial, "你好🌏", "你好🌏"+id))
		w.(http.Flusher).Flush()
		select {
		case <-s.finish:
			_, _ = io.WriteString(w, terminal)
			w.(http.Flusher).Flush()
		case <-request.Context().Done():
			return
		case <-release:
			return
		}
		// The peer must end this stream after its terminal, without tearing
		// down the shared HTTP/2 connection used by its siblings.
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	upstream.EnableHTTP2 = true
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			upstreamConnections.Add(1)
		}
	}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	upstreamClient := upstream.Client()
	upstreamClient.Transport.(*http.Transport).MaxConnsPerHost = 1
	t.Cleanup(upstreamClient.CloseIdleConnections)
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: slots + 1, User: slots, Key: slots, Channel: slots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	gateway, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), Client: upstreamClient,
		BillingMode: billing.ModeDisabled, Admission: CapacityAdmission{Capacity: capacity}, RetryTimes: 2, TotalTimeout: 12 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Close)
	type taggedResult struct {
		id string
		RequestResult
	}
	results := make(chan taggedResult, len(streams)+2)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test/:id", func(c *gin.Context) {
		binding := access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}}
		trusted := access.WithPrincipal(c.Request.Context(), binding)
		c.Request = c.Request.WithContext(trusted)
		id := c.Param("id")
		if operation == protocol.Chat {
			gateway.HandleChatCompletions(c)
			results <- taggedResult{id, *ResultFromContext(c.Request.Context())}
			return
		}
		body, readErr := io.ReadAll(c.Request.Body)
		if readErr != nil {
			c.Status(400)
			return
		}
		result := gateway.ExecuteTo(trusted, core.NewHTTPOutput(c.Writer), Execution{Operation: operation, ResponseID: "resp_local_" + id, Body: body})
		results <- taggedResult{id, result}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxyTransport := http.DefaultTransport.(*http.Transport).Clone()
	proxyTransport.ForceAttemptHTTP2 = false
	proxy.Transport = proxyTransport
	t.Cleanup(proxyTransport.CloseIdleConnections)
	edge := httptest.NewUnstartedServer(proxy)
	edge.EnableHTTP2 = true
	edge.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			edgeConnections.Add(1)
		}
	}
	edge.StartTLS()
	t.Cleanup(edge.Close)
	client := edge.Client()
	client.Transport.(*http.Transport).MaxConnsPerHost = 1
	t.Cleanup(client.CloseIdleConnections)
	t.Cleanup(func() {
		for _, s := range streams {
			if s.cancel != nil {
				s.cancel()
				_ = s.response.Body.Close()
			}
		}
	})
	seen := make(map[string]RequestResult)
	result := func(id string) RequestResult {
		t.Helper()
		for {
			if value, ok := seen[id]; ok {
				return value
			}
			select {
			case value := <-results:
				if _, duplicate := seen[value.id]; duplicate {
					t.Fatal("duplicate gateway result", value.id)
				}
				seen[value.id] = value.RequestResult
			case <-ctx.Done():
				t.Fatal("gateway did not finish stream", id)
			}
		}
	}
	waitEnd := func(s *stream) {
		t.Helper()
		select {
		case <-s.end:
		case <-ctx.Done():
			t.Fatal("upstream stream did not end", s.id)
		}
	}
	request := func(id string, requestCtx context.Context) *http.Response {
		t.Helper()
		body := fmt.Sprintf(`{"model":"m","stream":true,"messages":[{"role":"user","content":%q}]}`, id)
		if operation == protocol.Responses {
			body = fmt.Sprintf(`{"model":"m","stream":true,"input":%q,"store":false}`, id)
		}
		req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodPost, edge.URL+"/test/"+id, strings.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if response.ProtoMajor != 2 {
			t.Fatal("client did not negotiate HTTP/2", response.Proto)
		}
		return response
	}
	start := func(index int) *stream {
		t.Helper()
		s := streams[fmt.Sprintf("stream_%02d", index)]
		requestCtx, cancel := context.WithCancel(ctx)
		s.response = request(s.id, requestCtx)
		s.cancel = cancel
		if s.response.StatusCode != 200 {
			t.Fatal("stream was not admitted", s.id, s.response.StatusCode)
		}
		s.reader = bufio.NewReader(s.response.Body)
		for {
			line, readErr := s.reader.ReadString('\n')
			s.prefix += line
			if readErr != nil {
				t.Fatal("complete first output did not flush", s.id, readErr)
			}
			if line == "\n" && strings.Contains(s.prefix, "你好🌏"+s.id) {
				return s
			}
		}
	}
	occupancy := func(count int) {
		t.Helper()
		v := capacity.Snapshot()
		if v.Requests != count || v.Attempts != count || active.Load() != int64(count) {
			t.Fatalf("unexpected stream occupancy: capacity=%+v upstream=%d expected=%d", v, active.Load(), count)
		}
	}
	reject := func(id string) {
		t.Helper()
		before := hits.Load()
		response := request(id, ctx)
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		v := result(id)
		if response.StatusCode != 429 || readErr != nil || len(v.Attempts) != 0 || hits.Load() != before {
			t.Fatalf("full capacity sent/retried upstream: status=%d result=%+v error=%v", response.StatusCode, v, readErr)
		}
		occupancy(slots)
	}
	complete := func(s *stream) {
		t.Helper()
		close(s.finish)
		rest, readErr := io.ReadAll(s.reader)
		_ = s.response.Body.Close()
		s.cancel()
		v := result(s.id)
		waitEnd(s)
		text := s.prefix + string(rest)
		if readErr != nil || v.Error != nil || !v.Committed || !v.ProtocolComplete || v.StatusCode != 200 || len(v.Attempts) != 1 || s.hits.Load() != 1 {
			t.Fatalf("sibling/refill failed: id=%s result=%+v read=%v", s.id, v, readErr)
		}
		if strings.Count(text, "你好🌏"+s.id) != 1 || strings.Count(text, "你好🌏") != 1 || p0SocketToolArguments(t, []byte(text), operation) != `{"city":"上海"}` {
			t.Fatal("stream text/tools crossed request boundaries", s.id, text)
		}
		if operation == protocol.Chat && strings.Count(text, "[DONE]") != 1 ||
			operation == protocol.Responses && strings.Count(text, "event: response.completed") != 1 {
			t.Fatal("stream terminal was not delivered exactly once", s.id, text)
		}
		if operation == protocol.Responses && (!strings.Contains(text, "resp_local_"+s.id) || strings.Contains(text, "resp_upstream")) {
			t.Fatal("stream response identity changed", s.id, text)
		}
	}
	var first []*stream
	for i := 0; i < slots; i++ {
		first = append(first, start(i))
	}
	occupancy(slots)
	reject("rejected_before_cancel")
	for _, s := range first[:cancelled] {
		s.cancel()
		_ = s.response.Body.Close()
	}
	for _, s := range first[:cancelled] {
		v := result(s.id)
		waitEnd(s)
		if v.StatusCode != 499 || v.Error == nil || !v.Committed || v.ProtocolComplete || len(v.Attempts) != 1 {
			t.Fatalf("cancelled stream facts changed: %s %+v", s.id, v)
		}
	}
	occupancy(slots - cancelled)
	for _, s := range first[cancelled:] {
		select {
		case <-s.end:
			t.Fatal("cancelling a stream terminated its sibling", s.id)
		default:
		}
	}
	remaining := append([]*stream(nil), first[cancelled:]...)
	for i := slots; i < slots+cancelled; i++ {
		remaining = append(remaining, start(i))
	}
	occupancy(slots)
	reject("rejected_after_replacement")
	for _, s := range remaining {
		complete(s)
	}
	checkReleased(t, capacity)
	for i := slots + cancelled; i < slots*2+cancelled; i++ {
		start(i)
	}
	occupancy(slots)
	for i := slots + cancelled; i < slots*2+cancelled; i++ {
		complete(streams[fmt.Sprintf("stream_%02d", i)])
	}
	checkReleased(t, capacity)
	if active.Load() != 0 || hits.Load() != int64(len(streams)) || invalid.Load() != 0 ||
		upstreamConnections.Load() != 1 || edgeConnections.Load() != 1 || len(seen) != len(streams)+2 {
		t.Fatalf("HTTP/2 isolation/reuse: active=%d hits=%d invalid=%d upstreamConns=%d edgeConns=%d results=%d", active.Load(), hits.Load(), invalid.Load(), upstreamConnections.Load(), edgeConnections.Load(), len(seen))
	}
}
