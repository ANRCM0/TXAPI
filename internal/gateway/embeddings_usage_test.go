package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	authentication "minigateway/internal/foundation/identity"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
)

func TestEmbeddingsUsageStrictUnknownCannotSettleAsZero(t *testing.T) {
	for _, tc := range []struct {
		name, usage  string
		known, valid bool
		charge       int64
	}{
		{"input only", `{"prompt_tokens":11,"total_tokens":11}`, true, true, 220},
		{"explicit zero", `{"prompt_tokens":0,"total_tokens":0}`, true, true, 0},
		{"unknown", `null`, false, true, 0},
		{"empty", `{}`, false, true, 0},
		{"input missing", `{"total_tokens":11}`, false, true, 0},
		{"total missing", `{"prompt_tokens":11}`, false, true, 0},
		{"null input", `{"prompt_tokens":null,"total_tokens":0}`, false, true, 0},
		{"case aliases", `{"Prompt_Tokens":0,"Total_Tokens":0}`, false, true, 0},
		{"mismatch", `{"prompt_tokens":11,"total_tokens":12}`, false, false, 0},
		{"negative", `{"prompt_tokens":-1,"total_tokens":-1}`, false, false, 0},
		{"bad type", `{"prompt_tokens":"11","total_tokens":11}`, false, false, 0},
		{"duplicate", `{"prompt_tokens":11,"total_tokens":11,"total_tokens":0}`, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			wire := `{"object":"list","data":[{"embedding":"AQID","index":0}],"usage":` + tc.usage + `,"extension":"keep"}`
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				hits.Add(1)
				if r.URL.Path != "/v1/embeddings" {
					t.Error("embeddings changed operation", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, wire)
			}))
			defer upstream.Close()
			const actual = "text-embedding-3-small"
			if err := st.UpsertModelPrice(&dbmodel.ModelPrice{Model: actual, InputPricePer1K: 0.00002}); err != nil {
				t.Fatal(err)
			}
			channels := st.Channels()
			channels[0].BaseURL = "https://api.openai.com"
			channels[0].ModelMapping = dbmodel.StringMap{"gpt-4o-mini": actual}
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("0.1", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := engine.ConfigureProviderCosts("0.006", []billing.ProviderCostContract{{
				ChannelID: channels[0].ID, BaseURL: channels[0].BaseURL, Protocol: "openai", ActualModel: actual,
				Operation: "embeddings", InputTokenCeiling: 300000, InputBoundProfile: billing.OpenAIEmbeddingsV1,
				TariffProfile: billing.OpenAIStandardUSD20261017, Currency: "USD", CredentialBindings: testCredentialBindings(channels[0]),
				InputPricePer1K: "0.00002", OutputPricePer1K: "0", Evidence: "isolated loopback fixture, no real provider call",
			}}); err != nil {
				t.Fatal(err)
			}
			g, err := newRegisteredGateway(Options{Channels: channels, Client: verifiedLoopbackClient(t, upstream.URL), Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			router := gin.New()
			router.POST("/v1/embeddings", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), g.HandleEmbeddings)
			downstream := httptest.NewServer(router)
			defer downstream.Close()
			request, err := http.NewRequest(http.MethodPost, downstream.URL+"/v1/embeddings", bytes.NewBufferString(`{"model":"gpt-4o-mini","input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || hits.Load() != 1 || (response.StatusCode == 200) != tc.valid || tc.valid && string(data) != wire {
				t.Fatal("wire, attempt count or decode status changed", hits.Load(), response.StatusCode, err, string(data))
			}
			var reservation dbmodel.BillingReservation
			if err := st.DB().First(&reservation, "id=?", response.Header.Get("X-TXAPI-Billing-ID")).Error; err != nil {
				t.Fatal(err)
			}
			state, reserved := billing.Pending, int64(billing.UnitScale/10)
			if tc.known {
				state, reserved = billing.Settled, 0
			}
			if reservation.State != state || reservation.ChargeUnits != tc.charge || reservation.ProviderHeldUnits != 6_000_000 {
				t.Fatal("unknown usage settled or cost proof changed", reservation)
			}
			var attempts []dbmodel.BillingAttempt
			if err := st.DB().Where("reservation_id=?", reservation.ID).Find(&attempts).Error; err != nil || len(attempts) != 1 || attempts[0].CostKnown != tc.known {
				t.Fatal("unknown receipt became known or was replayed", attempts, err)
			}
			var balances []dbmodel.BillingBalance
			if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
				t.Fatal("missing dual balances", balances, err)
			}
			for _, balance := range balances {
				if balance.UsedUnits != tc.charge || balance.ReservedUnits != reserved {
					t.Fatal("unknown hold was cleared or input was charged twice", balance)
				}
			}
		})
	}
}
