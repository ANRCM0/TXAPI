package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/server/middleware"
)

type unpricedWireAdaptor struct {
	adaptor.Adaptor
	adaptor.ProtocolAdaptor
	body []byte
}

func (a *unpricedWireAdaptor) ConvertRequest([]byte, *meta.Meta) ([]byte, error) {
	return a.body, nil
}

func (a *unpricedWireAdaptor) BuildRequest(*canonical.ChatRequest) ([]byte, error) {
	return a.body, nil
}

func TestStrictPreparedDimensionsRejectBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, wire string
		embeddings, legacy   bool
	}{
		{"openai tier", "openai", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"service_tier":"priority"}`, false, false},
		{"embedding token batch", "openai", `{"model":"gpt-4o-mini","input":[[1,2],[3,4]]}`, true, false},
		{"claude cache", "anthropic", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"max_tokens":32}`, false, false},
		{"claude thinking", "anthropic", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":32,"thinking":{"type":"enabled","budget_tokens":1024}}`, false, false},
		{"gemini thinking", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32,"thinkingConfig":{"thinkingBudget":1024}}}`, false, false},
		{"gemini cache", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32},"cachedContent":"cached/x"}`, false, false},
		{"legacy transparent forwarding", "openai", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"service_tier":"priority"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestGateway(t, 1)
			st := fixture.store
			channels := st.Channels()
			channels[0].Type = tc.protocol
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			mode := billing.ModeLegacy
			if !tc.legacy {
				mode = billing.ModeStrict
				if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
					t.Fatal(err)
				}
				configureTestProviderCosts(t, engine, channels)
			}
			base, _ := protocol.Resolve(tc.protocol)
			custom := &unpricedWireAdaptor{Adaptor: base, ProtocolAdaptor: base.(adaptor.ProtocolAdaptor), body: []byte(tc.wire)}
			var health atomic.Int64
			gw, err := newRegisteredGateway(Options{Channels: channels, Billing: engine, BillingMode: mode, RetryTimes: 3,
				Protocols:      protocol.Protocols(map[string]adaptor.Adaptor{tc.protocol: custom}),
				HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			path, body := "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`
			router := gin.New()
			if tc.embeddings {
				path, body = "/v1/embeddings", `{"model":"gpt-4o-mini","input":["one","two"]}`
				router.POST(path, middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)
			} else {
				router.POST(path, middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
			}
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if tc.legacy {
				if w.Code != 200 || atomic.LoadInt64(fixture.upstreamHit) != 1 {
					t.Fatalf("legacy forwarding changed: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 400 || !strings.Contains(w.Body.String(), "unsupported_billing_dimension") || atomic.LoadInt64(fixture.upstreamHit) != 0 || health.Load() != 0 {
				t.Fatalf("unpriced conversion escaped: status=%d hits=%d health=%d body=%s", w.Code, *fixture.upstreamHit, health.Load(), w.Body.String())
			}
			detail, err := engine.ReservationDetail(req.Context(), w.Header().Get("X-TXAPI-Billing-ID"))
			if err != nil || len(detail.Attempts) != 0 || detail.Reservation.Reason != "unsupported_billing_dimension" || detail.Reservation.State != billing.Settled || detail.Reservation.ChargeUnits != "0" || detail.Reservation.ProviderHeldUnits != "0" {
				t.Fatalf("unpriced conversion changed durable money: %+v %v", detail, err)
			}
		})
	}
}
