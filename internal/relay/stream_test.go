package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	dbmodel "minigateway/internal/model"
	"minigateway/internal/relay/meta"
	relaymodel "minigateway/internal/relay/model"
)

func TestCommittedStreamFailureIsNotRetriedOrMarkedDone(t *testing.T) {
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

	oldRegistry, oldRetries := reg, cfgRetryTimes
	defer func() {
		reg, cfgRetryTimes = oldRegistry, oldRetries
	}()
	SetRetryTimes(2)
	if err := Init([]dbmodel.Channel{{
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
	Relay(ctx, &relaymodel.ModelRequest{Model: "gpt-4o-mini", Stream: true}, body, nil, meta.ModeChatCompletions, time.Second)

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
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"temporarily unavailable"}}`)
	}))
	defer upstream.Close()

	oldRegistry, oldRetries := reg, cfgRetryTimes
	defer func() {
		reg, cfgRetryTimes = oldRegistry, oldRetries
	}()
	SetRetryTimes(0)
	if err := Init([]dbmodel.Channel{{
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
	Relay(ctx, &relaymodel.ModelRequest{Model: "gpt-4o-mini"}, body, nil, meta.ModeChatCompletions, time.Second)

	if got := hits.Load(); got != 1 {
		t.Fatalf("retry_times=0 时上游请求数 = %d, want 1", got)
	}
}
