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
	"minigateway/internal/protocol"
)

// Both sides use real HTTP sockets; the downstream executes the production L2
// Chat handler through the existing testHTTPService adapter into the gateway.
func newP4ConvertedSocketGateway(t *testing.T, upstream, provider string) *p0SocketGateway {
	t.Helper()
	fixture := &p0SocketGateway{results: make(chan RequestResult, 1)}
	channels := lifecycleChannels(upstream)
	channels[0].Type = provider
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Channels: channels, RetryTimes: 2, TotalTimeout: 10 * time.Second,
		Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
			fixture.admitted.Add(1)
			return func() { fixture.released.Add(1) }, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test", func(c *gin.Context) {
		c.Request = c.Request.WithContext(access.WithPrincipal(c.Request.Context(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}}))
		g.HandleChatCompletions(c)
		fixture.results <- *ResultFromContext(c.Request.Context())
	})
	fixture.server = httptest.NewServer(router)
	t.Cleanup(func() { fixture.server.Close(); g.Close() })
	return fixture
}

func p4ConvertedSocketRequest(t *testing.T, f *p0SocketGateway, ctx context.Context, provider string) *http.Response {
	t.Helper()
	body := `{"model":"m","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]`
	if provider == "anthropic" {
		body += `,"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`
	}
	body += "}"
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

// Add a legal JSON whitespace newline using multiple SSE data fields. Every
// event uses CRLF, and a comment precedes its data. Anthropic message_stop ends
// the stream. Gemini finishReason ends a candidate; trailing usage is consumed
// before normal EOF completes the stream.
func p4ConvertedEvent(event, data string) string {
	prefix := ": provider heartbeat\r\n"
	if event != "" {
		prefix += "event: " + event + "\r\n"
	}
	if before, after, ok := strings.Cut(data, ","); ok {
		return prefix + "data: " + before + ",\r\ndata: " + after + "\r\n\r\n"
	}
	return prefix + "data: " + data + "\r\n\r\n"
}

func p4ConvertedSocketFrames(provider string) (partial, terminal string) {
	if provider == "gemini" {
		return p4ConvertedEvent("", `{"candidates":[{"content":{"parts":[{"text":"你好🌏"}],"role":"model"}}],"modelVersion":"m"}`),
			p4ConvertedEvent("", `{"candidates":[{"content":{"parts":[{"text":"！"}],"role":"model"},"finishReason":"STOP"}]}`)
	}
	partial = p4ConvertedEvent("message_start", `{"type":"message_start","message":{"id":"msg_socket","model":"m","role":"assistant","usage":{"input_tokens":2,"output_tokens":0}}}`) +
		p4ConvertedEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"你"}}`) +
		p4ConvertedEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好🌏"}}`) +
		p4ConvertedEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		p4ConvertedEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_weather","name":"weather","input":{}}}`) +
		p4ConvertedEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"上"}}`) +
		p4ConvertedEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"海\"}"}}`) +
		p4ConvertedEvent("content_block_stop", `{"type":"content_block_stop","index":1}`)
	terminal = p4ConvertedEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`) +
		p4ConvertedEvent("message_stop", `{"type":"message_stop"}`)
	return
}

func p4ConvertedDownstreamText(t *testing.T, body []byte) (text, finish, toolID, toolName string) {
	t.Helper()
	var output strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("invalid Chat chunk: %v data=%q", err, data)
		}
		for _, choice := range chunk.Choices {
			output.WriteString(choice.Delta.Content)
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			for _, call := range choice.Delta.ToolCalls {
				if call.ID != "" {
					toolID += call.ID
				}
				if call.Function.Name != "" {
					toolName += call.Function.Name
				}
			}
		}
	}
	return output.String(), finish, toolID, toolName
}

func TestP4ConvertedSSESocketMultilineNativeTerminal(t *testing.T) {
	for _, provider := range []string{"anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			partial, terminal := p4ConvertedSocketFrames(provider)
			wire := partial + terminal
			if provider == "gemini" {
				wire += p4ConvertedEvent("", `{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`)
			}
			closed := make(chan struct{})
			release := make(chan struct{})
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				for _, b := range []byte(wire) {
					if _, err := w.Write([]byte{b}); err != nil {
						close(closed)
						return
					}
					w.(http.Flusher).Flush()
				}
				if provider == "anthropic" {
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}
				close(closed)
			}))
			defer upstream.Close()
			defer close(release)
			fixture := newP4ConvertedSocketGateway(t, upstream.URL, provider)
			resp := p4ConvertedSocketRequest(t, fixture, context.Background(), provider)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			result := fixture.result(t)
			if resp.StatusCode != 200 || result.StatusCode != 200 || !result.ProtocolComplete || result.Error != nil || hits.Load() != 1 || len(result.Attempts) != 1 {
				t.Fatalf("terminal lost: HTTP=%d result=%+v hits=%d body=%s", resp.StatusCode, result, hits.Load(), body)
			}
			if !utf8.Valid(body) || strings.Count(string(body), "data: [DONE]") != 1 {
				t.Fatalf("invalid UTF-8 or terminal: %s", body)
			}
			text, finish, toolID, toolName := p4ConvertedDownstreamText(t, body)
			if provider == "anthropic" {
				if text != "你好🌏" || finish != "tool_calls" || toolID != "call_weather" || toolName != "weather" || p0SocketToolArguments(t, body, protocol.Chat) != `{"city":"上海"}` {
					t.Fatalf("Anthropic conversion changed: text=%q finish=%q id=%q name=%q body=%s", text, finish, toolID, toolName, body)
				}
			} else if text != "你好🌏！" || finish != "stop" || toolID != "" || toolName != "" {
				t.Fatalf("Gemini conversion changed: text=%q finish=%q body=%s", text, finish, body)
			}
			if result.Usage == nil || result.Usage.PromptTokens != 2 || result.Usage.CompletionTokens != 3 || result.Usage.TotalTokens != 5 {
				t.Fatalf("native usage changed: %+v", result.Usage)
			}
			if !strings.Contains(string(body), `"prompt_tokens":2`) || !strings.Contains(string(body), `"completion_tokens":3`) || !strings.Contains(string(body), `"total_tokens":5`) {
				t.Fatalf("usage missing from downstream stream: %s", body)
			}
			// Anthropic still holds release: only closing the attempt response can
			// make its upstream return. Gemini returns normally after trailing usage;
			// treating its candidate finish as stream completion would lose that usage.
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("native terminal left upstream open")
			}
		})
	}
}

func TestP4ConvertedSSESocketTruncatedTerminalAndEventError(t *testing.T) {
	for _, provider := range []string{"anthropic", "gemini"} {
		for _, failure := range []string{"truncated_terminal", "event_error_without_type"} {
			t.Run(provider+"/"+failure, func(t *testing.T) {
				partial, terminal := p4ConvertedSocketFrames(provider)
				var hits atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, partial)
					w.(http.Flusher).Flush()
					if failure == "truncated_terminal" {
						io.WriteString(w, strings.TrimSuffix(terminal, "\r\n\r\n")+"\r\n")
					} else {
						io.WriteString(w, p4ConvertedEvent("error", `{"message":"upstream error"}`))
					}
				}))
				defer upstream.Close()
				fixture := newP4ConvertedSocketGateway(t, upstream.URL, provider)
				resp := p4ConvertedSocketRequest(t, fixture, context.Background(), provider)
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				result := fixture.result(t)
				if result.StatusCode != 502 || result.ProtocolComplete || result.Error == nil || hits.Load() != 1 || len(result.Attempts) != 1 {
					t.Fatalf("failure became successful/retried: result=%+v hits=%d body=%s", result, hits.Load(), body)
				}
				if strings.Contains(string(body), "[DONE]") {
					t.Fatalf("failure invented DONE: %s", body)
				}
			})
		}
	}
}

func TestP4ConvertedSSESocketCancellationReleasesUpstream(t *testing.T) {
	for _, provider := range []string{"anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			partial, _ := p4ConvertedSocketFrames(provider)
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
			fixture := newP4ConvertedSocketGateway(t, upstream.URL, provider)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp := p4ConvertedSocketRequest(t, fixture, ctx, provider)
			if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			cancel()
			resp.Body.Close()
			result := fixture.result(t)
			if result.StatusCode != 499 || result.ProtocolComplete || hits.Load() != 1 || len(result.Attempts) != 1 {
				t.Fatalf("cancel lost/retried: %+v hits=%d", result, hits.Load())
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("cancel left upstream open")
			}
		})
	}
}
