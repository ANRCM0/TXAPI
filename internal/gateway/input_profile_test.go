package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"minigateway/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
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

type embeddingProfileAdaptor struct {
	openai.Adaptor
	url, model string
}

func (a *embeddingProfileAdaptor) GetRequestURL(m *meta.Meta) (string, error) {
	if a.url != "" {
		return a.url, nil
	}
	return a.Adaptor.GetRequestURL(m)
}

func (a *embeddingProfileAdaptor) ConvertRequest(body []byte, m *meta.Meta) ([]byte, error) {
	if a.model != "" {
		copy := *m
		copy.ActualModel = a.model
		m = &copy
	}
	return a.Adaptor.ConvertRequest(body, m)
}

func TestStrictEmbeddingInputProfileBindsWireAndWholeRequestCap(t *testing.T) {
	for _, tc := range []struct {
		name, url, model string
		batch            int
		wantStatus       int
	}{
		{"single batch", "", "", 2, 200},
		{"full batch", "", "", 2048, 200},
		{"oversized batch", "", "", 2049, 400},
		{"changed destination", "https://proxy.invalid/v1/embeddings", "", 2, 400},
		{"changed operation", "https://api.openai.com/v1/chat/completions", "", 2, 400},
		{"changed model", "", "unproved-model", 2, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestGateway(t, 1)
			st := fixture.store
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
				InputPricePer1K: "0.00002", OutputPricePer1K: "0", Evidence: "isolated tariff fixture, no actual provider call",
			}}); err != nil {
				t.Fatal(err)
			}
			hits, health := 0, 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hits++
				if r.URL.String() != "https://api.openai.com/v1/embeddings" {
					t.Errorf("unbound endpoint reached transport: %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":5,"total_tokens":5}}`))}, nil
			})}
			gw, err := newRegisteredGateway(Options{Channels: channels, Billing: engine, BillingMode: billing.ModeStrict, Client: client,
				Protocols:      protocol.Protocols(map[string]adaptor.Adaptor{"openai": &embeddingProfileAdaptor{url: tc.url, model: tc.model}}),
				HealthReporter: func(*dbmodel.Credential, int, string) { health++ },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer gw.Close()
			input := make([]string, tc.batch)
			for i := range input {
				input[i] = "hi"
			}
			body, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "input": input})
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+fixture.tokenKey)
			router := gin.New()
			router.POST("/v1/embeddings", middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			wantHits := 0
			if tc.wantStatus == 200 {
				wantHits = 1
			}
			if w.Code != tc.wantStatus || hits != wantHits || health != 0 {
				t.Fatalf("profile escaped: status=%d hits=%d health=%d body=%s", w.Code, hits, health, w.Body.String())
			}
			detail, err := engine.ReservationDetail(req.Context(), w.Header().Get("X-TXAPI-Billing-ID"))
			if err != nil || detail.Reservation.State != billing.Settled || len(detail.Attempts) != wantHits {
				t.Fatalf("profile accounting: %+v %v", detail, err)
			}
			if wantHits == 0 {
				if detail.Reservation.ProviderHeldUnits != "0" || detail.Reservation.ChargeUnits != "0" || detail.Reservation.Reason != "provider_cost_limit" {
					t.Fatalf("local refusal consumed cost: %+v", detail)
				}
			} else if detail.Reservation.ProviderHeldUnits != "6000000" || detail.Reservation.ChargeUnits != "100" || !strings.Contains(detail.Attempts[0].ProviderContractSnapshot, `"input_bound_profile":"openai_embeddings_v1"`) {
				t.Fatalf("did not reserve whole request cap or pinned profile: %+v", detail)
			}
		})
	}
}
