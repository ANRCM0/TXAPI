package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
)

func TestStrictConvertedLimitsBeforeUpstreamAndDurableAttempt(t *testing.T) {
	responses := map[string]string{
		"openai":    `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		"anthropic": `{"id":"msg-test","type":"message","role":"assistant","model":"mapped","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}}`,
		"gemini":    `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`,
	}
	for _, name := range []string{"openai", "anthropic", "gemini"} {
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			for _, mode := range []billing.Mode{billing.ModeStrict, billing.ModeLegacy} {
				t.Run(string(mode)+"/"+name+"/"+field, func(t *testing.T) {
					fixture := newTestGateway(t, 1)
					st := fixture.store
					var hits atomic.Int64
					type wireRequest struct {
						body []byte
						path string
					}
					wire := make(chan wireRequest, 1)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						body, _ := io.ReadAll(r.Body)
						wire <- wireRequest{body: body, path: r.URL.Path}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, responses[name])
					}))
					defer upstream.Close()
					table := billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
					engine := billing.New(st.DB(), table)
					if mode == billing.ModeStrict {
						if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
							t.Fatal(err)
						}
					}
					channels := st.Channels()
					channels[0].Type, channels[0].BaseURL = name, upstream.URL
					channels[0].ModelMapping = dbmodel.StringMap{"gpt-4o-mini": "mapped"}
					if mode == billing.ModeStrict {
						configureTestProviderCosts(t, engine, channels)
					}
					gw, err := newRegisteredGateway(Options{Channels: channels, Billing: engine, BillingMode: mode, RetryTimes: 3})
					if err != nil {
						t.Fatal(err)
					}
					defer gw.Close()
					router := gin.New()
					router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
					body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"` + field + `":32}`
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
					req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					reject := field == "max_completion_tokens" && name != "openai"
					wantStatus, wantHits := 200, int64(1)
					if reject {
						wantStatus, wantHits = 400, 0
					} else if mode == billing.ModeStrict {
						wantStatus, wantHits = 503, 0
					}
					if w.Code != wantStatus || hits.Load() != wantHits {
						t.Fatalf("unsafe forwarding: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
					}
					if wantHits > 0 {
						sentRequest := <-wire
						var sent struct {
							Model string `json:"model"`
						}
						if name == "gemini" {
							if !strings.Contains(sentRequest.path, "/models/mapped:") {
								t.Fatalf("Gemini URL mapping lost: %s", sentRequest.path)
							}
						} else if json.Unmarshal(sentRequest.body, &sent) != nil || sent.Model != "mapped" {
							t.Fatalf("mapping lost: %s", sentRequest.body)
						}
					}
					if mode != billing.ModeStrict {
						return
					}
					var row dbmodel.BillingReservation
					id := w.Header().Get("X-TXAPI-Billing-ID")
					if err := st.DB().First(&row, "id=?", id).Error; err != nil {
						t.Fatal(err)
					}
					var attempts int64
					st.DB().Model(&dbmodel.BillingAttempt{}).Where("reservation_id=?", id).Count(&attempts)
					if row.State != billing.Settled || attempts != wantHits {
						t.Fatalf("incorrect durable execution facts: reservation=%+v attempts=%d", row, attempts)
					}
					if row.ChargeUnits != 0 {
						t.Fatalf("unexecuted request charged: %+v", row)
					}
					if reject && row.Reason != "unsupported_billing_dimension" {
						t.Fatalf("conversion refusal lost from ledger: %+v", row)
					}
					if !reject && row.Reason != "provider_input_unproven" {
						t.Fatalf("unproved input refusal lost from ledger: %+v", row)
					}
					var balances []dbmodel.BillingBalance
					if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
						t.Fatalf("missing dual budget facts: %+v %v", balances, err)
					}
					for _, balance := range balances {
						if balance.ReservedUnits != 0 || balance.UsedUnits != row.ChargeUnits {
							t.Fatalf("budget leaked or duplicated: %+v", balance)
						}
					}
				})
			}
		}
	}
}

func configureTestProviderCosts(t *testing.T, engine *billing.Engine, channels []dbmodel.Channel) {
	t.Helper()
	var contracts []billing.ProviderCostContract
	for _, ch := range channels {
		for _, origin := range ch.Models {
			actual := origin
			if mapped := ch.ModelMapping[origin]; mapped != "" {
				actual = mapped
			}
			fields := []string{"max_tokens"}
			if ch.Type == "openai" {
				fields = append(fields, "max_completion_tokens")
			}
			for _, field := range fields {
				contracts = append(contracts, billing.ProviderCostContract{
					ChannelID: ch.ID, BaseURL: ch.BaseURL, Protocol: ch.Type, ActualModel: actual,
					Operation: "chat", OutputLimitField: field, InputTokenCeiling: 512, MaxOutputTokens: 4096,
					InputPricePer1K: "0.01", OutputPricePer1K: "0.02", Evidence: "isolated fake upstream contract, not a real provider",
				})
			}
		}
	}
	if err := engine.ConfigureProviderCosts("0.1", contracts); err != nil {
		t.Fatal(err)
	}
}

func TestStrictMalformedRequestsDoNotReserveOrForward(t *testing.T) {
	fixture := newTestGateway(t, 1)
	st := fixture.store
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	gw, err := newRegisteredGateway(Options{Channels: st.Channels(), Billing: engine, BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
	router.POST("/v1/embeddings", middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"max_tokens":4096}`},
		{"/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":null}],"max_tokens":32}`},
		{"/v1/embeddings", `{"model":"gpt-4o-mini","input":["hi",null]}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 || w.Header().Get("X-TXAPI-Billing-ID") != "" {
			t.Fatalf("invalid request reached reservation: %d %s", w.Code, w.Body.String())
		}
	}
	var reservations, attempts int64
	st.DB().Model(&dbmodel.BillingReservation{}).Count(&reservations)
	st.DB().Model(&dbmodel.BillingAttempt{}).Count(&attempts)
	if reservations != 0 || attempts != 0 || atomic.LoadInt64(fixture.upstreamHit) != 0 {
		t.Fatalf("invalid request caused monetary/network side effects: reservations=%d attempts=%d", reservations, attempts)
	}
}

func TestStrictEmbeddingsCannotBecomeCrossProtocolChat(t *testing.T) {
	for _, name := range []string{"anthropic", "gemini"} {
		t.Run(name, func(t *testing.T) {
			fixture := newTestGateway(t, 1)
			st := fixture.store
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			channels := st.Channels()
			channels[0].Type = name
			gw, err := newRegisteredGateway(Options{Channels: channels, Billing: engine, BillingMode: billing.ModeStrict})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			router := gin.New()
			router.POST("/v1/embeddings", middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewBufferString(`{"model":"gpt-4o-mini","input":["hi","there"]}`))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 400 || atomic.LoadInt64(fixture.upstreamHit) != 0 {
				t.Fatalf("embedding converted into unpriced chat: %d %s", w.Code, w.Body.String())
			}
			var row dbmodel.BillingReservation
			if err := st.DB().First(&row, "id=?", w.Header().Get("X-TXAPI-Billing-ID")).Error; err != nil ||
				row.State != billing.Settled || row.ChargeUnits != 0 {
				t.Fatalf("unexecuted embedding retained budget: %+v %v", row, err)
			}
		})
	}
}
