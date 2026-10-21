package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestStrictResponsesHTTPPreparedRequestUsesResponsesPolicy(t *testing.T) {
	for _, budget := range []string{"0.10", "1"} {
		for _, stream := range []bool{false, true} {
			for _, usage := range []string{"known", "zero", "missing"} {
				t.Run(fmt.Sprintf("budget=%s/stream=%t/%s", budget, stream, usage), func(t *testing.T) {
					f := newNativeServiceFixture(t)
					var hits atomic.Int64
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						var body map[string]json.RawMessage
						if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture-old-key" || json.NewDecoder(r.Body).Decode(&body) != nil || string(body["model"]) != `"actual"` || body["messages"] != nil || body["tools"] == nil {
							t.Error("prepared Responses target, credentials or function definitions changed")
						}
						response := `{"id":"resp_upstream","object":"response","model":"actual","status":"completed","output":[]`
						switch usage {
						case "known":
							response += `,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}`
						case "zero":
							response += `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
						}
						response += `}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":%s}\n\n", response)
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, response)
						}
					}))
					defer upstream.Close()
					f.channels[0].BaseURL = upstream.URL
					for _, name := range []string{"alias", "actual"} {
						if err := f.store.UpsertModelPrice(&model.ModelPrice{Model: name, InputPricePer1K: 0.15, OutputPricePer1K: 0.6}); err != nil {
							t.Fatal(err)
						}
					}
					engine := billing.New(f.store.DB(), billing.NewPriceTableWithHistory(f.store.PriceData, time.Hour, f.store.RecordPriceSnapshot))
					if err := engine.EnableStrict(budget, time.Minute); err != nil {
						t.Fatal(err)
					}
					policy := billing.ProviderCostContract{ChannelID: 1, BaseURL: upstream.URL, Protocol: "openai", ActualModel: "actual", Operation: "responses", OutputLimitField: "max_output_tokens", InputTokenCeiling: 4096, InputBoundProfile: billing.OperatorResponsesV1, MaxOutputTokens: 32, InputPricePer1K: "0.001", OutputPricePer1K: "0.002", Evidence: "controlled HTTP Responses function request and whole-input bound", TariffProfile: billing.OperatorDeclaredTokensV1, Currency: "USD", CredentialBindings: testCredentialBindings(f.channels[0]), PolicyVersion: "http-responses-fixture", PolicyExpiresAt: time.Now().Add(time.Hour).Unix(), MaxRequestBytes: 4096, AdditionalCostPerAttempt: "0.0001"}
					if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{policy}); err != nil {
						t.Fatal(err)
					}
					g, err := newRegisteredGateway(Options{Channels: f.channels, Client: upstream.Client(), Billing: engine, StrictBilling: engine, BillingMode: billing.ModeStrict})
					if err != nil {
						t.Fatal(err)
					}
					defer g.Close()
					body := fmt.Appendf(nil, `{"model":"alias","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],"max_output_tokens":32,"store":false,"stream":%t}`, stream)
					result := g.ExecuteTo(f.trusted, &observerOutput{}, Execution{Operation: protocol.Responses, ResponseID: "resp_local", Body: body})
					if budget == "0.10" {
						detail, err := engine.ReservationDetail(f.trusted, result.BillingID)
						if err != nil || result.StatusCode != 400 || hits.Load() != 0 || len(detail.Attempts) != 0 || detail.Reservation.State != billing.Settled || detail.Reservation.ChargeUnits != "0" || detail.Reservation.ProviderHeldUnits != "0" || detail.Reservation.Reason != "billing_fee_limit" {
							t.Fatalf("whole-input selling bound escaped admission: result=%+v detail=%+v hits=%d err=%v", result, detail, hits.Load(), err)
						}
						return
					}
					if result.StatusCode != 200 || !result.ProtocolComplete || hits.Load() != 1 || result.ActualModel != "actual" {
						t.Fatalf("strict Responses refused before its native consumer: hits=%d result=%+v", hits.Load(), result)
					}
					detail, err := engine.ReservationDetail(f.trusted, result.BillingID)
					if err != nil || len(detail.Attempts) != 1 || detail.Attempts[0].ActualModel != "actual" || detail.Attempts[0].ProviderContractVersion == "" {
						t.Fatalf("prepared attempt policy was not persisted: %+v %v", detail, err)
					}
					wantState, wantCharge := billing.Settled, "900000"
					if usage == "zero" {
						wantCharge = "0"
					}
					if usage == "missing" {
						wantState, wantCharge = billing.Pending, "0"
					}
					if detail.Reservation.State != wantState || detail.Reservation.ChargeUnits != wantCharge || detail.Attempts[0].CostKnown != (usage != "missing") {
						t.Fatalf("usage completeness changed financial facts: %+v", detail)
					}
					var balances []model.BillingBalance
					if err := f.store.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
						t.Fatalf("missing owner/key balances: %+v %v", balances, err)
					}
					for _, balance := range balances {
						if usage == "missing" {
							if balance.ReservedUnits == 0 || balance.UsedUnits != 0 || detail.Reservation.ProviderHeldUnits == "0" {
								t.Fatalf("unknown usage released a hold: %+v %+v", balance, detail)
							}
						} else if balance.ReservedUnits != 0 || fmt.Sprint(balance.UsedUnits) != wantCharge {
							t.Fatalf("known usage did not settle both balances: %+v", balance)
						}
					}
				})
			}
		}
	}
}
