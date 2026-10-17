package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/meta"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/server/middleware"
)

func TestS5BillingModesAtModelBoundary(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeDisabled, billing.ModeStrict} {
		for _, dependency := range []string{"present", "absent", "broken"} {
			t.Run(string(mode)+"/"+dependency, func(t *testing.T) {
				fixture := newTestGateway(t, 1)
				token, err := fixture.store.GetToken(1)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.DB().Model(token).Update("used_quota", 1).Error; err != nil {
					t.Fatal(err)
				}
				engine := fixture.billing
				if dependency == "absent" {
					engine = nil
				} else if dependency == "broken" {
					// A mode that touches the old quota subsystem will panic.
					engine = billing.New(nil, nil)
				}
				var logs []*dbmodel.RequestLog
				gw, err := New(Options{Channels: fixture.store.Channels(), Billing: engine, BillingMode: mode, LogRecorder: func(row *dbmodel.RequestLog) {
					logs = append(logs, row)
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer gw.Close()
				router := gin.New()
				router.POST("/v1/chat/completions", middleware.NewAuth(fixture.store).Handle(), gw.HandleChatCompletions)
				router.POST("/v1/embeddings", middleware.NewAuth(fixture.store).Handle(), gw.HandleEmbeddings)
				for _, path := range []string{"/v1/chat/completions", "/v1/embeddings"} {
					w := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"model":"gpt-4o-mini","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`))
					req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
					router.ServeHTTP(w, req)
					want := http.StatusOK
					if mode == billing.ModeStrict {
						want = http.StatusServiceUnavailable
						if !strings.Contains(w.Body.String(), `"billing_unavailable"`) {
							t.Fatalf("strict failure is not explicit: %s", w.Body.String())
						}
					}
					if w.Code != want {
						t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
					}
				}
				wantHits := int64(2)
				if mode == billing.ModeStrict {
					wantHits = 0
				}
				if got := atomic.LoadInt64(fixture.upstreamHit); got != wantHits {
					t.Fatalf("mode %s forwarded %d requests, want %d", mode, got, wantHits)
				}
				token, err = fixture.store.GetToken(token.ID)
				if err != nil || token.UsedQuota != 1 {
					t.Fatalf("mode %s modified quota: %+v err=%v", mode, token, err)
				}
				if len(logs) != 2 {
					t.Fatalf("lost request observation: %d", len(logs))
				}
				for _, row := range logs {
					if row.Cost != 0 || row.PriceMissing || row.PriceVersion != "" {
						t.Fatalf("unbilled request has a charge: %+v", row)
					}
					if mode == billing.ModeDisabled && row.TotalTokens != 18 {
						t.Fatalf("disabled billing also disabled usage: %+v", row)
					}
				}
			})
		}
	}
}

func TestS5StrictDirectRelayCannotBypassGate(t *testing.T) {
	gw, err := New(Options{BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	gw.Relay(c, &relaymodel.ModelRequest{Model: "m"}, nil, nil, meta.ModeChatCompletions, time.Second)
	if w.Code != http.StatusServiceUnavailable || len(state(c).Attempts) != 0 {
		t.Fatalf("direct relay bypassed strict gate: status=%d result=%+v", w.Code, state(c))
	}
	if _, err := New(Options{BillingMode: "typo"}); err == nil {
		t.Fatal("unknown billing mode accepted")
	}
}

func TestS5GatewayPinsPersistedPriceVersion(t *testing.T) {
	fixture := newTestGateway(t, 0)
	st := fixture.store
	table := billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	initial := table.Snapshot().Version()
	var entry *dbmodel.RequestLog
	gw, err := New(Options{
		Channels: st.Channels(), Billing: billing.New(st.DB(), table),
		LogRecorder: func(row *dbmodel.RequestLog) { entry = row },
		// Change the applied price after pre-deduction, before the HTTP attempt.
		Picker: func(creds []dbmodel.Credential, _ map[uint]bool, _ string, _ time.Time) (*dbmodel.Credential, bool) {
			err := st.UpsertModelPrice(&dbmodel.ModelPrice{Model: "gpt-4o-mini", InputPricePer1K: 100, OutputPricePer1K: 100})
			if err == nil {
				err = table.ReloadPrices()
			}
			if err != nil {
				t.Fatal(err)
			}
			return &creds[0], true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || entry == nil || entry.Cost != 0.00585 || entry.PriceVersion != initial {
		t.Fatalf("request repriced: status=%d entry=%+v", w.Code, entry)
	}
	if table.Snapshot().Version() == initial {
		t.Fatal("test did not publish a new price")
	}
	var history dbmodel.BillingPriceVersion
	if err := st.DB().First(&history, "version=?", entry.PriceVersion).Error; err != nil {
		t.Fatal("charged with an unpersisted price", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(history.Payload), &doc); err != nil || doc["unit_scale"] != float64(billing.UnitScale) {
		t.Fatalf("cannot audit price snapshot: %v", err)
	}
}

func TestS5StrictGatewaySettlesWithoutOrdinaryLogs(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			fixture := newTestGateway(t, 1)
			st := fixture.store
			table := billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
			engine := billing.New(st.DB(), table)
			if err := engine.EnableStrict("0.1", time.Second*10); err != nil {
				t.Fatal(err)
			}
			configureTestProviderCosts(t, engine, st.Channels())
			gw, err := New(Options{Channels: st.Channels(), Billing: engine, BillingMode: billing.ModeStrict})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			router := gin.New()
			router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
			body, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "messages": []map[string]string{{"role": "user", "content": "hi"}}, "max_tokens": 32, "stream": stream})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("strict call failed: %d %s", w.Code, w.Body.String())
			}
			id := w.Header().Get("X-TXAPI-Billing-ID")
			var reservation dbmodel.BillingReservation
			if len(id) != 32 || st.DB().First(&reservation, "id=?", id).Error != nil {
				t.Fatal("strict request lost durable receipt")
			}
			if reservation.State != billing.Settled || reservation.ChargeUnits != 5_850_000 ||
				reservation.PriceVersion != w.Header().Get("X-TXAPI-Price-Version") {
				t.Fatalf("incorrect strict receipt: %+v", reservation)
			}
			var attempt dbmodel.BillingAttempt
			if err := st.DB().First(&attempt, "reservation_id=?", id).Error; err != nil || !attempt.CostKnown || attempt.CostUnits != reservation.ChargeUnits {
				t.Fatalf("attempt cost lost: %+v err=%v", attempt, err)
			}
			var count int64
			st.DB().Model(&dbmodel.RequestLog{}).Count(&count)
			if count != 0 {
				t.Fatal("test unexpectedly relied on ordinary logs")
			}
		})
	}
}

func TestS5StrictGatewayMissingUsageHoldsBudget(t *testing.T) {
	fixture := newTestGateway(t, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"response without usage"}}]}`))
	}))
	defer upstream.Close()
	channels := append([]dbmodel.Channel(nil), fixture.store.Channels()...)
	channels[0].BaseURL = upstream.URL
	table := billing.NewPriceTableWithHistory(fixture.store.PriceData, time.Hour, fixture.store.RecordPriceSnapshot)
	engine := billing.New(fixture.store.DB(), table)
	if err := engine.EnableStrict("0.1", time.Second*10); err != nil {
		t.Fatal(err)
	}
	configureTestProviderCosts(t, engine, channels)
	var entry *dbmodel.RequestLog
	gw, err := New(Options{Channels: channels, Billing: engine, BillingMode: billing.ModeStrict, LogRecorder: func(row *dbmodel.RequestLog) { entry = row }})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.NewAuth(fixture.store).Handle(), gw.HandleChatCompletions)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`))
	req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
	router.ServeHTTP(w, req)
	if w.Code != 200 || entry == nil || entry.BillingState != billing.Pending || !entry.IsEstimated {
		t.Fatalf("missing usage silently finalized: %d %+v", w.Code, entry)
	}
	var row dbmodel.BillingReservation
	if err := fixture.store.DB().First(&row, "id=?", entry.BillingID).Error; err != nil || row.ReservedUnits != 100_000_000 || row.State != billing.Pending {
		t.Fatalf("missing usage became free: %+v err=%v", row, err)
	}
}
