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

func TestStrictFeeBoundBeforeNetworkAndEveryRetry(t *testing.T) {
	for _, tc := range []struct {
		name, ceiling, actual string
		rate                  float64
		retry                 bool
		wantHits              int64
		wantStatus            int
	}{
		{"one unit below full fee", "0.095999999", "", 1, false, 0, 400},
		{"exact full fee", "0.096", "", 1, false, 1, 200},
		{"mapped expensive selling price", "0.1", "expensive", 1, false, 0, 400},
		{"group multiplier", "0.1", "", 2, false, 0, 400},
		{"retry group becomes expensive", "0.1", "", 1, true, 1, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestGateway(t, 1)
			st := fixture.store
			var hits, health atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if tc.retry {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"message":"unknown failed attempt"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
			}))
			defer upstream.Close()
			if tc.actual != "" {
				if err := st.UpsertModelPrice(&dbmodel.ModelPrice{Model: tc.actual, InputPricePer1K: 1, OutputPricePer1K: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.SetGroupRates(map[string]float64{"selected": tc.rate, "retry-expensive": 2}); err != nil {
				t.Fatal(err)
			}
			channels := st.Channels()
			channels[0].BaseURL, channels[0].GroupName = upstream.URL, "selected"
			if tc.actual != "" {
				channels[0].ModelMapping = dbmodel.StringMap{"gpt-4o-mini": tc.actual}
			}
			if tc.retry {
				first := channels[0]
				backup := first
				backup.ID, backup.Name, backup.GroupName = 1001, "expensive-backup", "retry-expensive"
				backup.Priority = first.Priority - 1
				backup.Credentials = []dbmodel.Credential{{ID: 1002, APIKey: "isolated-backup-key", Status: "active", Weight: 1}}
				channels = append(channels, backup)
			}
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict(tc.ceiling, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			configureTestProviderCosts(t, engine, channels)
			gw, err := New(Options{Channels: channels, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 2,
				HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			router := gin.New()
			router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
				`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			wantHealth := int64(0)
			if tc.retry {
				wantHealth = 1
			}
			if w.Code != tc.wantStatus || hits.Load() != tc.wantHits || health.Load() != wantHealth {
				t.Fatalf("fee admission escaped: status=%d hits=%d health=%d body=%s", w.Code, hits.Load(), health.Load(), w.Body.String())
			}
			if tc.wantStatus == 400 && !strings.Contains(w.Body.String(), "billing_fee_limit") {
				t.Fatalf("missing local fee refusal: %s", w.Body.String())
			}
			id := w.Header().Get("X-TXAPI-Billing-ID")
			detail, err := engine.ReservationDetail(req.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(detail.Attempts)) != tc.wantHits {
				t.Fatalf("local refusal recorded as an execution: %+v", detail)
			}
			wantReason := ""
			if tc.wantStatus == 400 {
				wantReason = "billing_fee_limit"
				if tc.retry {
					wantReason = "execution_or_usage_unknown:" + wantReason
				}
			}
			if detail.Reservation.Reason != wantReason {
				t.Fatalf("fee refusal lost from admin ledger: %+v", detail.Reservation)
			}
			if tc.retry {
				if detail.Reservation.State != billing.Pending || detail.Reservation.ProviderHeldUnits != "5760000" {
					t.Fatalf("retry fee refusal released unknown first execution: %+v", detail)
				}
				return
			}
			if detail.Reservation.State != billing.Settled {
				t.Fatalf("terminal accounting missing: %+v", detail)
			}
			wantCharge := "0"
			if tc.wantHits == 1 {
				wantCharge = "5850000"
			}
			if detail.Reservation.ChargeUnits != wantCharge {
				t.Fatalf("fee check changed actual charging: %+v", detail)
			}
			if tc.wantHits == 1 {
				var snapshot struct {
					Format              int    `json:"format"`
					SellingBoundUnits   string `json:"selling_bound_units"`
					SellingPriceVersion string `json:"selling_price_version"`
				}
				if err := json.Unmarshal([]byte(detail.Attempts[0].ProviderContractSnapshot), &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.Format != 2 || snapshot.SellingBoundUnits != "96000000" || snapshot.SellingPriceVersion != detail.Reservation.PriceVersion {
					t.Fatalf("fee admission proof not persisted: %+v", snapshot)
				}
			}
		})
	}
}

func TestStrictFeeBoundEmbeddingsRequiresWholeBatchCoverage(t *testing.T) {
	fixture := newTestGateway(t, 1)
	st := fixture.store
	channels := st.Channels()
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConfigureProviderCosts("0.1", []billing.ProviderCostContract{{
		ChannelID: channels[0].ID, BaseURL: channels[0].BaseURL, Protocol: "openai", ActualModel: "gpt-4o-mini",
		Operation: "embeddings", InputTokenCeiling: 1000, InputPricePer1K: "0.001", OutputPricePer1K: "0",
		Evidence: "isolated whole-batch fixture",
	}}); err != nil {
		t.Fatal(err)
	}
	gw, err := New(Options{Channels: channels, Billing: engine, BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/embeddings", middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewBufferString(
		`{"model":"gpt-4o-mini","input":["one","two","three"]}`))
	req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "billing_fee_limit") || atomic.LoadInt64(fixture.upstreamHit) != 0 {
		t.Fatalf("embedding fee check used zero output or short strings only: %d %s", w.Code, w.Body.String())
	}
}
