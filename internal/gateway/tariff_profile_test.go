package gateway

import (
	"bytes"
	"context"
	"fmt"
	authentication "minigateway/internal/foundation/identity"
	"minigateway/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/server/middleware"
)

type tariffHeaderAdaptor struct {
	openai.Adaptor
	header string
}

func (a *tariffHeaderAdaptor) SetupRequestHeader(req *http.Request, m *meta.Meta) error {
	if err := a.Adaptor.SetupRequestHeader(req, m); err != nil {
		return err
	}
	if a.header == "Authorization" {
		req.Header.Set(a.header, "Bearer sk-substituted-by-adaptor-only")
	} else if a.header != "" {
		req.Header.Set(a.header, "unverified-project-or-org")
	}
	return nil
}

func TestTariffAdmissionRefusesDriftBeforeNetworkAndPreservesEarlierUnknown(t *testing.T) {
	for _, tc := range []struct{ name, header, reason string }{
		{"missing tariff", "", "provider_tariff_unproven"},
		{"rotated key", "", "provider_credential_unproven"},
		{"new credential", "", "provider_credential_unproven"},
		{"prepared auth differs", "Authorization", "provider_credential_unproven"},
		{"prepared project differs", "OpenAI-Project", "provider_credential_unproven"},
	} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/earlier_unknown=%v", tc.name, unknown), func(t *testing.T) {
				fixture := newTestGateway(t, 0)
				st := fixture.store
				var hits, health atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"error":{"message":"unknown execution"}}`))
				}))
				defer upstream.Close()
				channels, client := verifiedChatChannels(t, st.Channels(), upstream.URL)
				channels[0].Priority = 10
				if unknown {
					backup := channels[0]
					backup.ID, backup.Name, backup.Priority = 1001, "tariff-backup", 9
					backup.Credentials = []dbmodel.Credential{{ID: 1002, APIKey: "sk-backup-before-proof", Status: "active", Weight: 1}}
					channels = append(channels, backup)
				}
				target := len(channels) - 1
				contracts := make([]billing.ProviderCostContract, len(channels))
				for i, ch := range channels {
					contracts[i] = verifiedChatContract(ch)
				}
				switch tc.name {
				case "missing tariff":
					contracts[target].TariffProfile, contracts[target].Currency, contracts[target].CredentialBindings = "", "", nil
				case "rotated key":
					channels[target].Credentials[0].APIKey = "sk-rotated-after-proof-only"
				case "new credential":
					channels[target].Credentials[0].ID = 1003
				}
				engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
				if err := engine.EnableStrict("20", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := engine.ConfigureProviderCosts("1", contracts); err != nil {
					t.Fatal(err)
				}
				adaptors := map[string]adaptor.Adaptor{}
				if tc.header != "" {
					// Only the second channel's adaptor is changed, keeping the
					// first attempt real and unknown before the local refusal.
					channels[target].Type = "header-fixture"
					adaptors["header-fixture"] = &tariffHeaderAdaptor{header: tc.header}
				}
				gw, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 3,
					Protocols: protocol.Protocols(adaptors), HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) },
				})
				if err != nil {
					t.Fatal(err)
				}
				defer gw.Close()
				router := gin.New()
				router.POST("/v1/chat/completions", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), gw.HandleChatCompletions)
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"service_tier":"default","max_completion_tokens":32}`))
				req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				wantHits, wantState, wantReason, wantHeld, wantReserved := int64(0), billing.Settled, tc.reason, "0", int64(0)
				if unknown {
					wantHits, wantState, wantReason, wantHeld, wantReserved = 1, billing.Pending, "execution_or_usage_unknown:"+tc.reason, "19219200", 20_000_000_000
				}
				if w.Code != 503 || !strings.Contains(w.Body.String(), tc.reason) || hits.Load() != wantHits || health.Load() != wantHits {
					t.Fatalf("tariff drift reached executor/retry/health: status=%d hits=%d health=%d body=%s", w.Code, hits.Load(), health.Load(), w.Body.String())
				}
				detail, err := engine.ReservationDetail(context.Background(), w.Header().Get("X-TXAPI-Billing-ID"))
				if err != nil || detail.Reservation.State != wantState || detail.Reservation.Reason != wantReason || detail.Reservation.ProviderHeldUnits != wantHeld || detail.Reservation.ChargeUnits != "0" || int64(len(detail.Attempts)) != wantHits {
					t.Fatalf("tariff drift changed financial authority: %+v %v", detail, err)
				}
				var balances []dbmodel.BillingBalance
				if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
					t.Fatalf("balances: %+v %v", balances, err)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != wantReserved || balance.UsedUnits != 0 {
						t.Fatalf("tariff drift balance: %+v", balance)
					}
				}
			})
		}
	}
}
