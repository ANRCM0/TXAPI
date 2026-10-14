package gateway

import (
	"encoding/json"
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

	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/adaptor/gemini"
	"minigateway/internal/protocol/meta"
	relaymodel "minigateway/internal/protocol/model"
)

func TestCommittedStreamFailureIsNotRetriedOrMarkedDone(t *testing.T) {
	gw := testCoordinator(t)
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	oldRegistry, oldRetries := gw.reg, gw.retryTimes
	defer func() {
		gw.reg, gw.retryTimes = oldRegistry, oldRetries
	}()
	gw.SetRetryTimes(2)
	if err := gw.initRegistry([]dbmodel.Channel{{
		ID: 1, Name: "stream-failure", Type: "openai", BaseURL: upstream.URL,
		Models: dbmodel.StringList{"gpt-4o-mini"}, Status: dbmodel.StatusEnabled, Weight: 1,
		Credentials: []dbmodel.Credential{
			{ID: 1, APIKey: "key-one", Status: dbmodel.CredentialActive, Weight: 1},
			{ID: 2, APIKey: "key-two", Status: dbmodel.CredentialActive, Weight: 1},
		},
	}}); err != nil {
		t.Fatalf("初始化渠道池失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"gpt-4o-mini","stream":true,"messages":[]}`)
	gw.Relay(ctx, &relaymodel.ModelRequest{Model: "gpt-4o-mini", Stream: true}, body, nil, meta.ModeChatCompletions, time.Second)

	if got := hits.Load(); got != 1 {
		t.Fatalf("已提交流发生错误后上游请求数 = %d, want 1", got)
	}
	if got := relayStatus(ctx); got != http.StatusBadGateway {
		t.Fatalf("日志状态 = %d, want 502", got)
	}
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("异常流被伪装为正常结束: %s", recorder.Body.String())
	}
}

func TestRetryTimesZeroStopsAfterFirstFailure(t *testing.T) {
	gw := testCoordinator(t)
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"temporarily unavailable"}}`)
	}))
	defer upstream.Close()

	oldRegistry, oldRetries := gw.reg, gw.retryTimes
	defer func() {
		gw.reg, gw.retryTimes = oldRegistry, oldRetries
	}()
	gw.SetRetryTimes(0)
	if err := gw.initRegistry([]dbmodel.Channel{{
		ID: 1, Name: "retry-zero", Type: "openai", BaseURL: upstream.URL,
		Models: []string{"gpt-4o-mini"}, Status: dbmodel.StatusEnabled, Weight: 1,
		Credentials: []dbmodel.Credential{
			{ID: 1, APIKey: "key-one", Status: dbmodel.CredentialActive, Weight: 1},
			{ID: 2, APIKey: "key-two", Status: dbmodel.CredentialActive, Weight: 1},
		},
	}}); err != nil {
		t.Fatalf("初始化渠道池失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"gpt-4o-mini","messages":[]}`)
	gw.Relay(ctx, &relaymodel.ModelRequest{Model: "gpt-4o-mini"}, body, nil, meta.ModeChatCompletions, time.Second)

	if got := hits.Load(); got != 1 {
		t.Fatalf("retry_times=0 时上游请求数 = %d, want 1", got)
	}
}

// Same client-controlled request IDs must never cause two concurrent streams to
// share a parser. Hold all readers at the same barrier after their first event.
func TestConcurrentStreamsWithSameRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const clients = 8
	for _, protocol := range []string{"gemini", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			arrived := make(chan struct{}, clients)
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct{ Messages []struct{ Content string } }
				_ = json.NewDecoder(r.Body).Decode(&input)
				label := r.URL.Query().Get("key")
				if protocol == "anthropic" {
					label = r.Header.Get("X-Api-Key")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == "gemini" {
					fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]}}]}\n\n", label)
				} else {
					fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"model\":\"claude-test\"}}\n\n", label)
				}
				w.(http.Flusher).Flush()
				arrived <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if protocol == "gemini" {
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}]},\"finishReason\":\"STOP\"}]}\n\n")
				} else {
					fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", label+"!")
					fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer upstream.Close()
			go func() {
				defer close(release)
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for i := 0; i < clients; i++ {
					select {
					case <-arrived:
					case <-timer.C:
						return
					}
				}
			}()
			var group sync.WaitGroup
			for i := 0; i < clients; i++ {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					label := fmt.Sprintf("stream-%d", i)
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					ctx.Request.Header.Set("X-Request-Id", "same-external-id")
					ctx.Set("request_id", "same-external-id")
					var a adaptor.Adaptor
					if protocol == "gemini" {
						a = &gemini.Adaptor{}
					} else {
						a = &anthropic.Adaptor{}
					}
					m := &meta.Meta{ChannelID: 1, ChannelName: "same-channel", BaseURL: upstream.URL, APIKey: label, ActualModel: "model-test", IsStream: true}
					url, err := a.GetRequestURL(m)
					if err != nil {
						t.Error(err)
						return
					}
					req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"messages":[]}`))
					if err != nil {
						t.Error(err)
						return
					}
					_ = a.SetupRequestHeader(req, m)
					client := &http.Client{Timeout: 5 * time.Second}
					resp, err := client.Do(req)
					if err != nil {
						t.Error(err)
						return
					}
					_, err = relayStream(ctx, a, resp, m)
					if err != nil {
						t.Error(err)
						return
					}
					var text strings.Builder
					for _, line := range strings.Split(recorder.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
							continue
						}
						var chunk struct {
							ID      string
							Choices []struct{ Delta struct{ Content string } }
						}
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
							t.Error(err)
							return
						}
						if protocol == "anthropic" && chunk.ID != label {
							t.Errorf("mixed stream ID: got %s, want %s", chunk.ID, label)
						}
						for _, choice := range chunk.Choices {
							text.WriteString(choice.Delta.Content)
						}
					}
					if text.String() != label+"!" || strings.Count(recorder.Body.String(), "data: [DONE]") != 1 {
						t.Errorf("stream %s: text=%q body=%s", label, text.String(), recorder.Body.String())
					}
				}(i)
			}
			group.Wait()
		})
	}
}

func TestConvertedStreamsRejectErrorsAfterFinish(t *testing.T) {
	cases := []struct {
		name   string
		a      adaptor.Adaptor
		finish string
		event  string
	}{
		{"gemini error", &gemini.Adaptor{}, `data: {"candidates":[{"finishReason":"STOP"}]}`, `data: {"error":{"code":500,"message":"upstream failed"}}`},
		{"gemini malformed", &gemini.Adaptor{}, `data: {"candidates":[{"finishReason":"STOP"}]}`, `data: {broken`},
		{"anthropic error", &anthropic.Adaptor{}, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `data: {"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`},
		{"anthropic malformed", &anthropic.Adaptor{}, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `data: {broken`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.finish + "\n\n" + tc.event + "\n\n"))}
			_, err := relayStream(ctx, tc.a, resp, &meta.Meta{ActualModel: "test-model"})
			if err == nil || strings.Contains(recorder.Body.String(), "[DONE]") {
				t.Fatalf("failed stream marked success: err=%v body=%s", err, recorder.Body.String())
			}
		})
	}
}
