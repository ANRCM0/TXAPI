package gateway

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

// The production HTTP adapters and executor run between two real HTTP sockets.
// Responses uses its registered gateway operation; durable L3 state is separate.
func metadataSocketGateway(t *testing.T, options Options, operation protocol.Operation) *p0SocketGateway {
	t.Helper()
	f := &p0SocketGateway{results: make(chan RequestResult, 1)}
	options.BillingMode = billing.ModeDisabled
	options.TotalTimeout = 3 * time.Second
	g, err := newRegisteredGateway(options)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/test", func(c *gin.Context) {
		c.Request = c.Request.WithContext(outputIdentity())
		// Simulate the existing request-ID middleware, before the adapter runs.
		c.Header("X-Request-ID", "local-request")
		switch operation {
		case protocol.Chat:
			g.HandleChatCompletions(c)
			f.results <- *ResultFromContext(c.Request.Context())
		case protocol.Embeddings:
			g.HandleEmbeddings(c)
			f.results <- *ResultFromContext(c.Request.Context())
		default:
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.Status(400)
				return
			}
			f.results <- g.ExecuteTo(c.Request.Context(), core.NewHTTPOutput(c.Writer), Execution{Operation: operation, Body: body, ResponseID: "resp_local"})
		}
	})
	f.server = httptest.NewServer(router)
	t.Cleanup(func() { f.server.Close(); g.Close() })
	return f
}

func metadataSocketCall(t *testing.T, f *p0SocketGateway, body string) (*http.Response, string, RequestResult) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, f.server.URL+"/test", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-f.results:
		return resp, string(wire), result
	case <-time.After(4 * time.Second):
		t.Fatal("handler did not finish")
		return nil, "", RequestResult{}
	}
}

func assertMetadataSocketHeaders(t *testing.T, resp *http.Response, id, retry string) {
	t.Helper()
	if resp.Header.Get("X-TXAPI-Upstream-Request-ID") != id || resp.Header.Get("Retry-After") != retry || resp.Header.Get("X-Request-ID") != "local-request" {
		t.Fatalf("unexpected selected/local headers: %v", resp.Header)
	}
	for _, key := range []string{"Authorization", "Set-Cookie", "WWW-Authenticate", "Location", "X-Organization-ID", "X-RateLimit-Remaining", "X-TXAPI-Billing-ID", "Content-Encoding", "Request-ID"} {
		if resp.Header.Get(key) != "" {
			t.Fatalf("unselected header leaked: %s=%q", key, resp.Header.Get(key))
		}
	}
}

func TestUpstreamMetadataSocketOperationsAndGzip(t *testing.T) {
	for _, tc := range []struct {
		name, provider          string
		op                      protocol.Operation
		stream, enabled, zipped bool
	}{
		{"default-off", "openai", protocol.Chat, false, false, false},
		{"chat-json", "openai", protocol.Chat, false, true, false},
		{"chat-sse-gzip", "openai", protocol.Chat, true, true, true},
		{"embeddings", "openai", protocol.Embeddings, false, true, true},
		{"responses-json", "openai", protocol.Responses, false, true, false},
		{"responses-sse", "openai", protocol.Responses, true, true, false},
		{"anthropic-sse", "anthropic", protocol.Chat, true, true, false},
		{"gemini-sse", "gemini", protocol.Chat, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			wire := `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
			body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
			if tc.op == protocol.Embeddings {
				wire = `{"data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}`
				body = `{"model":"m","input":"hello"}`
			} else if tc.op == protocol.Responses {
				wire = `{"id":"resp_upstream","object":"response","status":"completed","output":[]}`
				body = `{"model":"m","input":"hello","store":false}`
			}
			if tc.stream {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
				partial, terminal := p0SocketFrames(tc.op)
				if tc.provider != "openai" {
					partial, terminal = p4ConvertedSocketFrames(tc.provider)
				}
				wire = partial + terminal
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("X-Request-Id", "req-final")
				w.Header().Set("Retry-After", "9") // Never forwarded for success.
				for _, k := range []string{"Authorization", "Set-Cookie", "WWW-Authenticate", "Location", "X-Organization-ID", "X-RateLimit-Remaining", "X-TXAPI-Billing-ID"} {
					w.Header().Set(k, "upstream-secret")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				if tc.zipped {
					if r.Header.Get("Accept-Encoding") != "gzip" {
						t.Error("native transport did not request gzip")
					}
					w.Header().Set("Content-Encoding", "gzip")
					z := gzip.NewWriter(w)
					io.WriteString(z, wire)
					z.Close()
				} else {
					io.WriteString(w, wire)
				}
			}))
			defer upstream.Close()
			channels := lifecycleChannels(upstream.URL)
			channels[0].Type = tc.provider
			f := metadataSocketGateway(t, Options{Channels: channels, ForwardUpstreamMetadata: tc.enabled}, tc.op)
			resp, downstream, result := metadataSocketCall(t, f, body)
			id := ""
			if tc.enabled {
				id = "req-final"
			}
			assertMetadataSocketHeaders(t, resp, id, "")
			if resp.StatusCode != 200 || result.Error != nil || !result.ProtocolComplete || hits.Load() != 1 || len(result.Attempts) != 1 || downstream == "" {
				t.Fatalf("operation changed: status=%d body=%s hits=%d result=%+v", resp.StatusCode, downstream, hits.Load(), result)
			}
			if strings.Contains(downstream, "upstream-secret") {
				t.Fatal("secret reached response body")
			}
		})
	}
}

func TestUpstreamMetadataSocketErrorsAndHopExclusion(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		retry, connection, id string
		duplicate             bool
		wantID, wantRetry     string
	}{
		{"numeric", 429, "13", "", "req-error", false, "req-error", "13"},
		{"date", 503, "Wed, 21 Oct 2026 07:28:00 GMT", "", "req-error", false, "req-error", "Wed, 21 Oct 2026 07:28:00 GMT"},
		{"connection", 429, "13", "X-Request-ID, Retry-After", "req-error", false, "", ""},
		{"duplicate", 503, "13", "", "req-error", true, "", ""},
		{"long-id", 503, "-2", "", strings.Repeat("a", 129), false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("X-Request-ID", tc.id)
				w.Header().Set("Retry-After", tc.retry)
				if tc.connection != "" {
					w.Header().Set("Connection", tc.connection)
				}
				if tc.duplicate {
					w.Header().Add("X-Request-ID", "second")
					w.Header().Add("Retry-After", "2")
				}
				w.Header().Set("Set-Cookie", "secret")
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":{"message":"limited","type":"provider_error"}}`)
			}))
			defer upstream.Close()
			f := metadataSocketGateway(t, Options{Channels: lifecycleChannels(upstream.URL), ForwardUpstreamMetadata: true}, protocol.Chat)
			resp, _, result := metadataSocketCall(t, f, `{"model":"m","messages":[]}`)
			assertMetadataSocketHeaders(t, resp, tc.wantID, tc.wantRetry)
			if resp.StatusCode != tc.status || hits.Load() != 1 || len(result.Attempts) != 1 {
				t.Fatalf("error/retry changed: status=%d result=%+v", resp.StatusCode, result)
			}
		})
	}
}

func TestUpstreamMetadataSocketRetryIsolation(t *testing.T) {
	for _, final := range []string{"success", "no-response", "admission"} {
		t.Run(final, func(t *testing.T) {
			var hits, leases atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					w.Header().Set("X-Request-ID", "old")
					w.Header().Set("Retry-After", "99")
					w.WriteHeader(503)
					io.WriteString(w, `{"error":{"message":"retry"}}`)
					return
				}
				if final == "no-response" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.Header().Set("X-Request-ID", "new")
				io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			}))
			defer upstream.Close()
			f := metadataSocketGateway(t, Options{Channels: lifecycleChannels(upstream.URL), ForwardUpstreamMetadata: true, RetryTimes: 1, Admission: admissionFunc(func(_ context.Context, s AdmissionScope) (func(), error) {
				if final == "admission" && s.Level == AttemptAdmission && s.Attempt == 2 {
					return nil, &AdmissionError{StatusCode: 429, RetryAfterSeconds: 7}
				}
				leases.Add(1)
				return func() { leases.Add(-1) }, nil
			})}, protocol.Chat)
			resp, _, result := metadataSocketCall(t, f, `{"model":"m","messages":[]}`)
			id, retry, status, attempts, sends := "new", "", 200, 2, int64(2)
			if final == "no-response" {
				id, status = "", 502
			}
			if final == "admission" {
				id, retry, status, attempts, sends = "", "7", 429, 1, 1
			}
			assertMetadataSocketHeaders(t, resp, id, retry)
			if resp.StatusCode != status || hits.Load() != sends || len(result.Attempts) != attempts || leases.Load() != 0 {
				t.Fatalf("retry/leak facts status=%d hits=%d leases=%d result=%+v", resp.StatusCode, hits.Load(), leases.Load(), result)
			}
		})
	}
}

func TestUpstreamMetadataNonForegroundDoesNotObserve(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "private")
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), ForwardUpstreamMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, source := range []invocationSource{invocationUnspecified, invocationClaimed} {
		out := &observerOutput{}
		result := g.executeFrom(outputIdentity(), out, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[]}`)}, nil, false, source)
		if result.Error != nil || out.metadata.Values["X-TXAPI-Upstream-Request-ID"] != "" {
			t.Fatalf("nonforeground inherited opt-in: %+v %+v", result, out.metadata)
		}
	}
}

func TestUpstreamMetadataSocketCommittedFailureDoesNotRetry(t *testing.T) {
	var hits atomic.Int64
	partial, _ := p0SocketFrames(protocol.Chat)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "truncated")
		w.Header().Set("Retry-After", "99")
		io.WriteString(w, partial)
	}))
	defer upstream.Close()
	f := metadataSocketGateway(t, Options{Channels: lifecycleChannels(upstream.URL), ForwardUpstreamMetadata: true, RetryTimes: 2}, protocol.Chat)
	resp, body, result := metadataSocketCall(t, f, `{"model":"m","stream":true,"messages":[]}`)
	assertMetadataSocketHeaders(t, resp, "truncated", "")
	if resp.StatusCode != 200 || !result.Committed || result.ProtocolComplete || result.Error == nil || len(result.Attempts) != 1 || hits.Load() != 1 || strings.Contains(body, "[DONE]") {
		t.Fatalf("committed failure changed: status=%d body=%q hits=%d result=%+v", resp.StatusCode, body, hits.Load(), result)
	}
}
