package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
)

func lifecycleChannels(url string) []dbmodel.Channel {
	return []dbmodel.Channel{{ID: 1, Name: "test", Type: "openai", BaseURL: url, Models: dbmodel.StringList{"m"}, Weight: 1, Status: dbmodel.StatusEnabled, Credentials: []dbmodel.Credential{
		{ID: 1, APIKey: "one", Weight: 1, Status: dbmodel.CredentialActive}, {ID: 2, APIKey: "two", Weight: 1, Status: dbmodel.CredentialActive}, {ID: 3, APIKey: "three", Weight: 1, Status: dbmodel.CredentialActive},
	}}}
}
func relayContext(ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	return c, rec
}
func TestRetryAttemptsKeepTypedResultsAndReplayBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	var bodies []string
	var bodyMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyMu.Lock()
		bodies = append(bodies, string(body))
		bodyMu.Unlock()
		if hits.Add(1) == 1 {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"first attempt failed"}}`)
			return
		}
		io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	g, err := New(Options{Channels: lifecycleChannels(upstream.URL), RetryTimes: 2, TotalTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, rec := relayContext(context.Background())
	body := []byte(`{"model":"m","messages":[],"extra":{"keep":true}}`)
	g.Relay(c, &model.ModelRequest{Model: "m"}, body, nil, meta.ModeChatCompletions, time.Second)
	result := ResultFromContext(c.Request.Context())
	if rec.Code != 200 || result.StatusCode != 200 || result.ErrorMessage != "" || len(result.Attempts) != 2 {
		t.Fatalf("result=%+v code=%d", result, rec.Code)
	}
	if result.Attempts[0].StatusCode != 503 || result.Attempts[0].Committed || result.Attempts[1].StatusCode != 200 || !result.Attempts[1].Committed {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
	bodyMu.Lock()
	defer bodyMu.Unlock()
	if len(bodies) != 2 || bodies[0] != string(body) || bodies[1] != string(body) {
		t.Fatalf("body not faithfully replayed: %v", bodies)
	}
}
func TestTotalDeadlineBoundsAllAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(50 * time.Millisecond):
			w.WriteHeader(503)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	g, err := New(Options{Channels: lifecycleChannels(upstream.URL), RetryTimes: 2, TotalTimeout: 70 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, _ := relayContext(context.Background())
	started := time.Now()
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	result := ResultFromContext(c.Request.Context())
	if result.StatusCode != 504 || result.Termination != core.Deadline || len(result.Attempts) > 2 || time.Since(started) > 400*time.Millisecond {
		t.Fatalf("total deadline not enforced: %+v", result)
	}
}
func TestCanceledRequestDoesNotExecuteOrRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(503) }))
	defer upstream.Close()
	g, err := New(Options{Channels: lifecycleChannels(upstream.URL), RetryTimes: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := relayContext(ctx)
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	result := ResultFromContext(c.Request.Context())
	if hits.Load() != 0 || len(result.Attempts) != 0 || result.StatusCode != 499 {
		t.Fatalf("canceled request executed: result=%+v hits=%d", result, hits.Load())
	}
}
func TestIndependentGatewaysDoNotShareRuntimeState(t *testing.T) {
	a, err := New(Options{Channels: lifecycleChannels("http://a.test"), RetryTimes: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(Options{Channels: lifecycleChannels("http://b.test"), RetryTimes: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.Reload(nil); err != nil {
		t.Fatal(err)
	}
	if a.ChannelCount() != 0 || b.ChannelCount() != 1 || a.reg == b.reg || a.executor == b.executor || a.metrics == b.metrics {
		t.Fatal("instances share state")
	}
}

// Exercise a real client cancellation while the upstream blocks on another chunk.
func TestCanceledStreamingRequestClosesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	closed := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer upstream.Close()
	g, err := New(Options{Channels: lifecycleChannels(upstream.URL), RetryTimes: 2, TotalTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := relayContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Relay(c, &model.ModelRequest{Model: "m", Stream: true}, []byte(`{"model":"m","stream":true}`), nil, meta.ModeChatCompletions, time.Second)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled relay did not return")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream connection leaked")
	}
	result := ResultFromContext(c.Request.Context())
	if result.StatusCode != 499 || len(result.Attempts) != 1 {
		t.Fatalf("canceled stream retried: %+v", result)
	}
}

// Preserve mode dispatch and input transparency without any SQLite/Gin setup.
func ExampleRequestResult() {
	result := RequestResult{Attempts: []AttemptResult{{Result: core.Result{StatusCode: 503}}, {Result: core.Result{StatusCode: 200}}}}
	fmt.Println(len(result.Attempts))
	fmt.Println(strings.Join([]string{string(core.Success)}, ","))
	// Output:
	// 2
	// success
}
