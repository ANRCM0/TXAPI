package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

// Exercise the production Chat HTTP handler and native Responses coordinator
// over real upstream/downstream sockets. Responses persistence and the L3 public
// handler are deliberately outside this gateway transport regression's scope.
type p0SocketGateway struct {
	server   *httptest.Server
	results  chan RequestResult
	admitted atomic.Int64
	released atomic.Int64
}

func newP0SocketGateway(t *testing.T, upstream string, operation protocol.Operation) *p0SocketGateway {
	return newP0SocketGatewayWithOptions(t, upstream, operation, nil)
}

func newP0SocketGatewayWithOptions(t *testing.T, upstream string, operation protocol.Operation, configure func(*Options)) *p0SocketGateway {
	t.Helper()
	fixture := &p0SocketGateway{results: make(chan RequestResult, 1)}
	opts := Options{BillingMode: billing.ModeDisabled, Channels: lifecycleChannels(upstream), RetryTimes: 2, TotalTimeout: 10 * time.Second,
		Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
			fixture.admitted.Add(1)
			return func() { fixture.released.Add(1) }, nil
		}),
	}
	if configure != nil {
		configure(&opts)
	}
	g, err := newRegisteredGateway(opts)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test", func(c *gin.Context) {
		ctx := access.WithPrincipal(c.Request.Context(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
		c.Request = c.Request.WithContext(ctx)
		if operation == protocol.Chat {
			g.HandleChatCompletions(c)
			fixture.results <- *ResultFromContext(c.Request.Context())
			return
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(400)
			return
		}
		fixture.results <- g.ExecuteTo(ctx, core.NewHTTPOutput(c.Writer), Execution{Operation: operation, ResponseID: "resp_local", Body: body})
	})
	fixture.server = httptest.NewServer(router)
	t.Cleanup(func() { fixture.server.Close(); g.Close() })
	return fixture
}

func (f *p0SocketGateway) request(t *testing.T, ctx context.Context, operation protocol.Operation) *http.Response {
	t.Helper()
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	if operation == protocol.Responses {
		body = `{"model":"m","stream":true,"input":"hello","store":false}`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/test", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *p0SocketGateway) result(t *testing.T) RequestResult {
	t.Helper()
	select {
	case result := <-f.results:
		if f.admitted.Load() != 2 || f.released.Load() != 2 {
			t.Fatalf("admission leak: acquired=%d released=%d", f.admitted.Load(), f.released.Load())
		}
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("gateway handler did not release resources")
		return RequestResult{}
	}
}

func p0SocketFrames(operation protocol.Operation) (partial, terminal string) {
	if operation == protocol.Chat {
		return "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\n" +
				"data: \"delta\":{\"content\":\"你好🌏\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"上海\\\"}\"}}]},\"finish_reason\":null}]}\n\n",
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	}
	return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\n" +
			"data: \"delta\":\"你好🌏\"}\n\n" +
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\n" +
			"data: \"sequence_number\":1,\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\"{\\\"city\\\":\\\"上海\\\"}\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\n" +
			"data: \"response\":{\"id\":\"resp_upstream\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"
}

func p0SocketToolArguments(t *testing.T, body []byte, operation protocol.Operation) string {
	t.Helper()
	var arguments strings.Builder
	for _, event := range strings.Split(string(body), "\n\n") {
		var data []string
		for _, line := range strings.Split(event, "\n") {
			if value, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(value, " "))
			}
		}
		payload := strings.Join(data, "\n")
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Type    string `json:"type"`
			Delta   string `json:"delta"`
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid downstream SSE JSON: %v payload=%q", err, payload)
		}
		if operation == protocol.Responses && chunk.Type == "response.function_call_arguments.delta" {
			arguments.WriteString(chunk.Delta)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				arguments.WriteString(call.Function.Arguments)
			}
		}
	}
	return arguments.String()
}

func TestP0SSESocketMultilineTerminalReleasesOpenUpstream(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Responses} {
		t.Run(string(operation), func(t *testing.T) {
			partial, terminal := p0SocketFrames(operation)
			closed := make(chan struct{})
			release := make(chan struct{})
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				// Split every UTF-8 byte across writes and Flush calls, independently of SSE
				// lines/events; TCP is free to coalesce them again.
				for _, b := range []byte(partial + terminal) {
					if _, err := w.Write([]byte{b}); err != nil {
						close(closed)
						return
					}
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
				close(closed)
			}))
			defer upstream.Close()
			defer close(release)
			fixture := newP0SocketGateway(t, upstream.URL, operation)
			resp := fixture.request(t, context.Background(), operation)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			result := fixture.result(t)
			if resp.StatusCode != 200 || result.StatusCode != 200 || !result.ProtocolComplete || result.Error != nil || hits.Load() != 1 {
				t.Fatalf("terminal facts: HTTP=%d result=%+v hits=%d body=%s", resp.StatusCode, result, hits.Load(), body)
			}
			if !utf8.Valid(body) || !strings.Contains(string(body), "你好🌏") || !strings.Contains(string(body), "上海") {
				t.Fatalf("multiline UTF-8/tool data lost: %s", body)
			}
			if arguments := p0SocketToolArguments(t, body, operation); arguments != `{"city":"上海"}` {
				t.Fatalf("tool arguments changed: got %q", arguments)
			}
			if operation == protocol.Chat && strings.Count(string(body), "[DONE]") != 1 {
				t.Fatalf("Chat terminal changed: %s", body)
			}
			if operation == protocol.Responses && (!strings.Contains(string(body), "resp_local") || strings.Contains(string(body), "resp_upstream") || strings.Contains(string(body), "[DONE]")) {
				t.Fatalf("native terminal identity/framing changed: %s", body)
			}
			// release is still held. Only closing the response body/request can make the
			// upstream handler return, proving completion did not wait for transport EOF.
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal did not close upstream response")
			}
		})
	}
}

func TestP0SSESocketTruncationAndErrorsDoNotInventSuccess(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Responses} {
		for _, failure := range []string{"truncated", "malformed"} {
			t.Run(string(operation)+"/"+failure, func(t *testing.T) {
				partial, _ := p0SocketFrames(operation)
				var hits atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, partial)
					w.(http.Flusher).Flush()
					if failure == "malformed" {
						io.WriteString(w, "data: {invalid JSON}\n\n")
					}
				}))
				defer upstream.Close()
				fixture := newP0SocketGateway(t, upstream.URL, operation)
				resp := fixture.request(t, context.Background(), operation)
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				result := fixture.result(t)
				if result.StatusCode != 502 || result.ProtocolComplete || result.Error == nil || hits.Load() != 1 {
					t.Fatalf("failure presented as success/retried: result=%+v hits=%d body=%s", result, hits.Load(), body)
				}
				if strings.Contains(string(body), "[DONE]") || strings.Contains(string(body), "event: response.completed") {
					t.Fatalf("invented terminal: %s", body)
				}
			})
		}
	}
}

func TestP0SSESocketDownstreamCancelReleasesUpstream(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Responses} {
		t.Run(string(operation), func(t *testing.T) {
			partial, _ := p0SocketFrames(operation)
			closed := make(chan struct{})
			release := make(chan struct{})
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, partial)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
				close(closed)
			}))
			defer upstream.Close()
			defer close(release)
			fixture := newP0SocketGateway(t, upstream.URL, operation)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp := fixture.request(t, ctx, operation)
			// Receiving actual output ensures cancellation happens after commitment.
			one := make([]byte, 1)
			if _, err := io.ReadFull(resp.Body, one); err != nil {
				t.Fatal(err)
			}
			cancel()
			resp.Body.Close()
			result := fixture.result(t)
			if result.StatusCode != 499 || result.ProtocolComplete || hits.Load() != 1 {
				t.Fatalf("cancel facts/retry: %+v hits=%d", result, hits.Load())
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("downstream cancellation did not close upstream")
			}
		})
	}
}
