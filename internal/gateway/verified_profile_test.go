package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
)

const verifiedChatModel = "gpt-4o-mini-2024-07-18"

// Keep the verified destination visible to admission; only the test transport
// redirects the already-validated request to its isolated loopback server.
func verifiedChatChannels(t *testing.T, channels []dbmodel.Channel, localURL string) ([]dbmodel.Channel, *http.Client) {
	t.Helper()
	copy := append([]dbmodel.Channel(nil), channels...)
	for i := range copy {
		if copy[i].Type != "openai" {
			t.Fatal("verified chat fixture requires OpenAI")
		}
		copy[i].BaseURL = "https://api.openai.com"
		copy[i].ModelMapping = make(dbmodel.StringMap)
		for _, origin := range copy[i].Models {
			copy[i].ModelMapping[origin] = verifiedChatModel
		}
	}
	return copy, verifiedLoopbackClient(t, localURL)
}

func verifiedLoopbackClient(t *testing.T, localURL string) *http.Client {
	t.Helper()
	local, err := url.Parse(localURL)
	if err != nil || local.Scheme != "http" || local.Hostname() != "127.0.0.1" {
		t.Fatal("verified fixture requires an isolated loopback HTTP server")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" ||
			(r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/embeddings") {
			return nil, fmt.Errorf("unexpected fixture destination")
		}
		copy := r.Clone(r.Context())
		target := *r.URL
		target.Scheme, target.Host = local.Scheme, local.Host
		copy.URL = &target
		return transport.RoundTrip(copy)
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func configureVerifiedChatCosts(t *testing.T, engine *billing.Engine, channels []dbmodel.Channel) {
	t.Helper()
	var contracts []billing.ProviderCostContract
	for _, ch := range channels {
		contracts = append(contracts, verifiedChatContract(ch))
	}
	if err := engine.ConfigureProviderCosts("1", contracts); err != nil {
		t.Fatal(err)
	}
}

func verifiedChatContract(ch dbmodel.Channel) billing.ProviderCostContract {
	return billing.ProviderCostContract{
		ChannelID: ch.ID, BaseURL: ch.BaseURL, Protocol: "openai", ActualModel: verifiedChatModel,
		Operation: "chat", OutputLimitField: "max_completion_tokens", InputTokenCeiling: 128000, MaxOutputTokens: 4096,
		InputBoundProfile: billing.OpenAIChatV1, InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006",
		TariffProfile: billing.OpenAIStandardUSD20261017, Currency: "USD", CredentialBindings: testCredentialBindings(ch),
		Evidence: "isolated tariff fixture; transport never calls a real provider",
	}
}

func testCredentialBindings(ch dbmodel.Channel) map[string]string {
	bindings := make(map[string]string, len(ch.Credentials))
	for _, credential := range ch.Credentials {
		digest := sha256.Sum256([]byte(credential.APIKey))
		bindings[fmt.Sprint(credential.ID)] = hex.EncodeToString(digest[:])
	}
	return bindings
}

func TestUnprovedInputRefusesOnlyStrictWithoutNetworkOrHealthPenalty(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeStrict, billing.ModeLegacy, billing.ModeDisabled} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", mode, stream), func(t *testing.T) {
				fixture := newTestGateway(t, 1)
				st := fixture.store
				engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
				if mode == billing.ModeStrict {
					if err := engine.EnableStrict("0.1", time.Minute); err != nil {
						t.Fatal(err)
					}
					configureTestProviderCosts(t, engine, st.Channels())
				}
				var health atomic.Int64
				gw, err := New(Options{Channels: st.Channels(), Billing: engine, BillingMode: mode, RetryTimes: 3,
					HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) },
				})
				if err != nil {
					t.Fatal(err)
				}
				defer gw.Close()
				router := gin.New()
				router.POST("/v1/chat/completions", middleware.NewAuth(st).Handle(), gw.HandleChatCompletions)
				body, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "max_tokens": 32,
					"stream": stream, "messages": []map[string]string{{"role": "user", "content": "hi"}}})
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if mode != billing.ModeStrict {
					if w.Code != 200 || atomic.LoadInt64(fixture.upstreamHit) != 1 {
						t.Fatalf("unbilled/legacy compatibility regressed: %d %s", w.Code, w.Body.String())
					}
					return
				}
				if w.Code != 503 || !strings.Contains(w.Body.String(), "provider_input_unproven") ||
					strings.Contains(w.Body.String(), "[DONE]") || atomic.LoadInt64(fixture.upstreamHit) != 0 || health.Load() != 0 {
					t.Fatalf("unproved input escaped admission: status=%d hits=%d health=%d body=%s", w.Code, *fixture.upstreamHit, health.Load(), w.Body.String())
				}
				detail, err := engine.ReservationDetail(req.Context(), w.Header().Get("X-TXAPI-Billing-ID"))
				if err != nil || len(detail.Attempts) != 0 || detail.Reservation.State != billing.Settled ||
					detail.Reservation.Reason != "provider_input_unproven" || detail.Reservation.ChargeUnits != "0" || detail.Reservation.ProviderHeldUnits != "0" {
					t.Fatalf("unproved input changed financial truth: %+v %v", detail, err)
				}
				var balances []dbmodel.BillingBalance
				if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
					t.Fatalf("dual balances missing: %+v %v", balances, err)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != 0 || balance.UsedUnits != 0 {
						t.Fatalf("unexecuted refusal retained or charged funds: %+v", balance)
					}
				}
			})
		}
	}
}
