package generation

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestWorkerShutdownPreservesUnknownExecutionAndDualBudgetHolds(t *testing.T) {
	st, binding, _ := fixture(t)
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.00015, OutputPricePer1K: 0.0006}); err != nil {
		t.Fatal(err)
	}
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	ch := channels()
	ch[0].BaseURL = "https://api.openai.com"
	ch[0].ModelMapping = model.StringMap{"m": "gpt-4o-mini-2024-07-18"}
	if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{{
		ChannelID: ch[0].ID, BaseURL: ch[0].BaseURL, Protocol: "openai", ActualModel: "gpt-4o-mini-2024-07-18",
		Operation: "chat", OutputLimitField: "max_completion_tokens", InputTokenCeiling: 128000, MaxOutputTokens: 4096,
		InputBoundProfile: billing.OpenAIChatV1, InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006",
		TariffProfile: billing.OpenAIStandardUSD20261017, Currency: "USD",
		CredentialBindings: map[string]string{"1": access.Hash(ch[0].Credentials[0].APIKey)},
		Evidence:           "isolated cancellation fixture; no real provider network or price certification",
	}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict, RetryTimes: 2,
		Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://api.openai.com/v1/chat/completions" {
				t.Errorf("fixture reached an unbound destination: %s", req.URL)
			}
			hits.Add(1)
			close(started)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}})
	r := newRunner(t, st.ResolvePrincipal, g.ExecuteTo)
	out := &memoryOutput{}
	f, err := r.Submit(context.Background(), binding, out, gateway.Execution{Operation: protocol.Chat,
		Body: []byte(`{"model":"m","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	signal(t, started)
	closeRunner(t, r)
	result, err := wait(t, f)
	if err != nil || result.BillingState != billing.Pending || len(result.Attempts) != 1 || hits.Load() != 1 {
		t.Fatalf("shutdown refunded/replayed an uncertain attempt: %+v %v hits=%d", result, err, hits.Load())
	}
	var reservations []model.BillingReservation
	if err := st.DB().Find(&reservations).Error; err != nil || len(reservations) != 1 || reservations[0].ProviderHeldUnits <= 0 ||
		reservations[0].State != billing.Pending || reservations[0].ChargeUnits != 0 {
		t.Fatalf("worker lost durable unknown-execution facts: %+v %v", reservations, err)
	}
	var balances []model.BillingBalance
	if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
		t.Fatalf("worker lost dual financial authority: %+v %v", balances, err)
	}
	for _, balance := range balances {
		if balance.ReservedUnits <= 0 || balance.UsedUnits != 0 {
			t.Fatalf("unknown execution became free: %+v", balance)
		}
	}
	if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 {
		t.Fatalf("durable holds leaked process-local slots: %+v", got)
	}
}
