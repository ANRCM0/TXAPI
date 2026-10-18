package gateway

import (
	"bytes"
	authentication "minigateway/internal/foundation/identity"
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

func TestStrictProviderAdmissionBeforeNetworkAndAcrossRetries(t *testing.T) {
	for _, tc := range []struct {
		name, budget, failure string
		contract              bool
		output                int
		hits                  int64
	}{
		{"missing", "", "provider_cost_contract_missing", false, 32, 0},
		{"first envelope too large", "0.001", "provider_budget_exhausted", true, 32, 0},
		{"second envelope too large", "0.0300192", "provider_budget_exhausted", true, 32, 1},
		{"output above contract", "0.1", "provider_cost_limit", true, 33, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"message":"uncertain provider execution"}}`))
			}))
			defer upstream.Close()
			channels, client := verifiedChatChannels(t, st.Channels(), upstream.URL)
			channels[0].Credentials = append(channels[0].Credentials,
				dbmodel.Credential{ID: 1001, APIKey: "isolated-test-second-key", Status: "active", Weight: 1},
				dbmodel.Credential{ID: 1002, APIKey: "isolated-test-third-key", Status: "active", Weight: 1})
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("31", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			if tc.contract {
				err := engine.ConfigureProviderCosts(tc.budget, []billing.ProviderCostContract{{
					ChannelID: channels[0].ID, BaseURL: channels[0].BaseURL, Protocol: "openai", ActualModel: verifiedChatModel,
					Operation: "chat", OutputLimitField: "max_completion_tokens", InputTokenCeiling: 200000, MaxOutputTokens: 32,
					InputBoundProfile: billing.OpenAIChatV1,
					TariffProfile:     billing.OpenAIStandardUSD20261017, Currency: "USD", CredentialBindings: testCredentialBindings(channels[0]),
					InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006", Evidence: "isolated standard-rate fixture, no real provider call",
				}})
				if err != nil {
					t.Fatal(err)
				}
			}
			gw, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			router := gin.New()
			router.POST("/v1/chat/completions", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), gw.HandleChatCompletions)
			output := "32"
			if tc.output == 33 {
				output = "33"
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
				`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"service_tier":"default","max_completion_tokens":`+output+`}`))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			wantStatus := 503
			if tc.output > 32 {
				wantStatus = 400
			}
			if w.Code != wantStatus || !strings.Contains(w.Body.String(), tc.failure) || hits.Load() != tc.hits {
				t.Fatalf("unsafe provider execution: hits=%d status=%d body=%s", hits.Load(), w.Code, w.Body.String())
			}
			var row dbmodel.BillingReservation
			if err := st.DB().First(&row, "id=?", w.Header().Get("X-TXAPI-Billing-ID")).Error; err != nil {
				t.Fatal(err)
			}
			var attempts []dbmodel.BillingAttempt
			st.DB().Where("reservation_id=?", row.ID).Find(&attempts)
			if int64(len(attempts)) != tc.hits || row.ProviderHeldUnits != 30_019_200*tc.hits {
				t.Fatalf("nondurable budget/network mismatch: %+v %+v", row, attempts)
			}
			if tc.hits == 0 && (row.State != billing.Settled || row.ChargeUnits != 0) {
				t.Fatalf("unexecuted request was charged or held: %+v", row)
			}
			if tc.hits > 0 && (row.State != billing.Pending || row.ReservedUnits != 31_000_000_000) {
				t.Fatalf("uncertain first attempt was treated as free: %+v", row)
			}
		})
	}
}

func TestStrictProviderEmbeddingBatchUsesIndependentInputEnvelope(t *testing.T) {
	fixture := newTestGateway(t, 0)
	st := fixture.store
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("embedding became another operation: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"usage":{"prompt_tokens":11,"total_tokens":11}}`))
	}))
	defer upstream.Close()
	channels := st.Channels()
	channels[0].BaseURL = "https://api.openai.com"
	channels[0].ModelMapping = dbmodel.StringMap{"gpt-4o-mini": "text-embedding-3-small"}
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("46", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConfigureProviderCosts("0.1", []billing.ProviderCostContract{{
		ChannelID: channels[0].ID, BaseURL: channels[0].BaseURL, Protocol: "openai", ActualModel: "text-embedding-3-small",
		Operation: "embeddings", InputTokenCeiling: 300000, InputBoundProfile: billing.OpenAIEmbeddingsV1,
		TariffProfile: billing.OpenAIStandardUSD20261017, Currency: "USD", CredentialBindings: testCredentialBindings(channels[0]),
		InputPricePer1K: "0.00002", OutputPricePer1K: "0", Evidence: "isolated whole-batch fake upstream",
	}}); err != nil {
		t.Fatal(err)
	}
	gw, err := newRegisteredGateway(Options{Channels: channels, Client: verifiedLoopbackClient(t, upstream.URL), Billing: engine, BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/embeddings", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), gw.HandleEmbeddings)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewBufferString(
		`{"model":"gpt-4o-mini","input":["one","two","three"]}`))
	req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var row dbmodel.BillingReservation
	if err := st.DB().First(&row, "id=?", w.Header().Get("X-TXAPI-Billing-ID")).Error; err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || hits.Load() != 1 || row.State != billing.Settled || row.ChargeUnits != 1_650_000 || row.ProviderHeldUnits != 6_000_000 {
		t.Fatalf("embedding accounting regressed: status=%d hits=%d row=%+v body=%s", w.Code, hits.Load(), row, w.Body.String())
	}
}

func TestStrictProviderContractRejectsHotTargetDrift(t *testing.T) {
	fixture := newTestGateway(t, 1)
	st := fixture.store
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("0.1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	channels, client := verifiedChatChannels(t, st.Channels(), fixture.upstream.URL)
	configureVerifiedChatCosts(t, engine, channels)
	channels[0].ModelMapping = dbmodel.StringMap{"gpt-4o-mini": "new-mapped-model"}
	gw, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), gw.HandleChatCompletions)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"service_tier":"default","max_completion_tokens":32}`))
	req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "provider_cost_contract_missing") || atomic.LoadInt64(fixture.upstreamHit) != 0 {
		t.Fatalf("new target inherited an unrelated cost contract: %d %s", w.Code, w.Body.String())
	}
}
