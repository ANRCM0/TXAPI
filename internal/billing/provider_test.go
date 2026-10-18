package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func providerBillingFixture(t *testing.T) (*Engine, *store.Store, uint, uint) {
	t.Helper()
	e, st, user, key := strictFixture(t, 1, 1)
	// These provider-budget fixtures declare a 1000-token input envelope.
	// Its selling-price bound is 0.1692, independent of the cheap provider.
	if err := e.EnableStrict("0.2", time.Minute); err != nil {
		t.Fatal(err)
	}
	return e, st, user, key
}

func providerTestContract() ProviderCostContract {
	return ProviderCostContract{
		ChannelID: 1, BaseURL: "https://fixture.invalid", Protocol: "openai", ActualModel: "m",
		Operation: "chat", OutputLimitField: "max_tokens", InputTokenCeiling: 1000, MaxOutputTokens: 32,
		InputPricePer1K: "0.001", OutputPricePer1K: "0.002", Evidence: "isolated fixture contract",
	}
}

func providerTestTarget(c ProviderCostContract) ProviderTarget {
	return ProviderTarget{c.ChannelID, c.BaseURL, c.Protocol, c.ActualModel, c.Operation}
}

// Arithmetic/ledger fixtures use abstract tariffs, not real network admission.
// Public execution admission is covered separately with verified wire profiles.
func (e *Engine) startDeclaredTestAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, target ProviderTarget, request StrictRequest) (err error) {
	defer func() { err = e.RecordCostRefusal(ctx, pre, err) }()
	contract, err := e.lookupCostContract(pre, attempt, target, request)
	if err != nil {
		return err
	}
	return e.startContractAttempt(ctx, pre, attempt, contract, request)
}

func TestProviderContractValidationAndExactBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProviderCostContract)
	}{
		{"missing evidence", func(c *ProviderCostContract) { c.Evidence = "" }},
		{"alias wildcard", func(c *ProviderCostContract) { c.ActualModel = "*" }},
		{"no ceiling", func(c *ProviderCostContract) { c.InputTokenCeiling = 0 }},
		{"no input tariff", func(c *ProviderCostContract) { c.InputPricePer1K = "" }},
		{"negative tariff", func(c *ProviderCostContract) { c.OutputPricePer1K = "-1" }},
		{"exponent", func(c *ProviderCostContract) { c.OutputPricePer1K = "1e-3" }},
		{"sub unit", func(c *ProviderCostContract) { c.OutputPricePer1K = "0.0000000001" }},
		{"overflow tariff", func(c *ProviderCostContract) { c.InputPricePer1K = "9223372037" }},
		{"overflow bound", func(c *ProviderCostContract) { c.InputTokenCeiling = 1<<63 - 1; c.InputPricePer1K = "1" }},
		{"credentials in URL", func(c *ProviderCostContract) { c.BaseURL = "https://secret@fixture.invalid" }},
		{"unknown protocol", func(c *ProviderCostContract) { c.Protocol = "unknown" }},
		{"unknown operation", func(c *ProviderCostContract) { c.Operation = "responses" }},
		{"cross protocol completion", func(c *ProviderCostContract) { c.Protocol = "anthropic"; c.OutputLimitField = "max_completion_tokens" }},
		{"embedding output", func(c *ProviderCostContract) { c.Operation = "embeddings" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := providerTestContract()
			tc.edit(&c)
			if _, err := compileProviderCosts("0.1", []ProviderCostContract{c}); err == nil {
				t.Fatal("unsafe contract accepted")
			}
		})
	}
	c := providerTestContract()
	for _, budget := range []string{"", "0", "-1", "0.0000000001", "9223372037"} {
		if _, err := compileProviderCosts(budget, []ProviderCostContract{c}); err == nil {
			t.Fatalf("unsafe budget accepted: %s", budget)
		}
	}
	if _, err := compileProviderCosts("1", []ProviderCostContract{c, c}); err == nil {
		t.Fatal("duplicate exact contract accepted")
	}
	c.InputTokenCeiling, c.InputPricePer1K, c.OutputPricePer1K = 1, "0.000000001", "0.000000001"
	p, err := compileProviderCosts("1", []ProviderCostContract{c})
	if err != nil {
		t.Fatal(err)
	}
	contract := p.contracts[providerKey{providerTestTarget(c), c.OutputLimitField}]
	if units, err := contract.bound(1); err != nil || units != 1 {
		t.Fatalf("must ceil the aggregate fixed-point cost: %d %v", units, err)
	}
}

func TestProviderRetryEnvelopesPersistAndDoNotReprice(t *testing.T) {
	e, st, user, key := providerBillingFixture(t)
	configs := []ProviderCostContract{providerTestContract()}
	if err := e.ConfigureProviderCosts("0.002128", configs); err != nil {
		t.Fatal(err)
	}
	configs[0].InputPricePer1K = "0"
	if err := e.ConfigureProviderCosts("1", configs); err == nil {
		t.Fatal("policy mutated after publication")
	}
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	target := providerTestTarget(providerTestContract())
	limits := StrictRequest{32, "max_tokens"}
	attempt := model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"}
	if err := e.StartAttempt(ctx, pre, attempt); !errors.Is(err, ErrProviderContract) {
		t.Fatalf("ledger API bypassed provider admission: %v", err)
	}
	if err := e.startDeclaredTestAttempt(ctx, pre, attempt, target, limits); err != nil {
		t.Fatal(err)
	}
	// An uncertain failed attempt keeps its whole envelope.
	if err := e.EndAttempt(ctx, pre, 1, 503, "upstream_error", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.startDeclaredTestAttempt(ctx, pre, attempt, target, limits); err == nil {
		t.Fatal("duplicate durable attempt accepted")
	}
	var row model.BillingReservation
	st.DB().First(&row, "id=?", pre.BillingID())
	if row.ProviderHeldUnits != 1_064_000 {
		t.Fatalf("duplicate attempt leaked budget: %+v", row)
	}
	attempt.Number = 2
	if err := e.startDeclaredTestAttempt(ctx, pre, attempt, target, limits); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if err := e.EndAttempt(ctx, pre, 2, 200, "success", u); err != nil {
		t.Fatal(err)
	}
	attempt.Number = 3
	if err := e.startDeclaredTestAttempt(ctx, pre, attempt, target, limits); !errors.Is(err, ErrProviderBudget) {
		t.Fatalf("aggregate retry budget bypassed: %v", err)
	}
	charge, state, err := e.Finish(ctx, pre, u, "m", "default", true)
	if err != nil || state != Settled || charge != 5_850_000 {
		t.Fatalf("provider tariff replaced selling price: %d %s %v", charge, state, err)
	}
	detail, err := e.ReservationDetail(ctx, pre.BillingID())
	if err != nil {
		t.Fatal(err)
	}
	if detail.Reservation.ProviderBudgetUnits != "2128000" || detail.Reservation.ProviderHeldUnits != "2128000" || len(detail.Attempts) != 2 {
		t.Fatalf("durable cost history missing: %+v", detail)
	}
	for _, a := range detail.Attempts {
		hash := sha256.Sum256([]byte(a.ProviderContractSnapshot))
		if a.ProviderBoundUnits != "1064000" || a.ProviderContractVersion != hex.EncodeToString(hash[:]) ||
			!strings.Contains(a.ProviderContractSnapshot, `"input_price_per_1k":"0.001"`) {
			t.Fatalf("mutable/untraceable provider bound: %+v", a)
		}
	}
}

func TestProviderAdmissionIsAtomicBeforeAttemptCreation(t *testing.T) {
	e, st, user, key := providerBillingFixture(t)
	c := providerTestContract()
	if err := e.ConfigureProviderCosts("0.001064", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(number int) {
			defer wg.Done()
			err := e.startDeclaredTestAttempt(context.Background(), pre, model.BillingAttempt{Number: number, ChannelID: 1, ActualModel: "m"},
				providerTestTarget(c), StrictRequest{32, "max_tokens"})
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrProviderBudget) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var row model.BillingReservation
	st.DB().First(&row, "id=?", pre.BillingID())
	var count int64
	st.DB().Model(&model.BillingAttempt{}).Where("reservation_id=?", pre.BillingID()).Count(&count)
	if accepted.Load() != 1 || count != 1 || row.ProviderHeldUnits != 1_064_000 {
		t.Fatalf("provider budget overspent: accepted=%d attempts=%d row=%+v", accepted.Load(), count, row)
	}
}

func TestProviderAttemptWriteFailureRollsBackEnvelope(t *testing.T) {
	e, st, user, key := providerBillingFixture(t)
	c := providerTestContract()
	if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Callback().Create().Before("gorm:create").Register("provider-write-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "billing_attempts" {
			tx.AddError(errors.New("controlled attempt write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB().Callback().Create().Remove("provider-write-fault") })
	err = e.startDeclaredTestAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"},
		providerTestTarget(c), StrictRequest{32, "max_tokens"})
	if err == nil {
		t.Fatal("failed attempt write accepted")
	}
	var row model.BillingReservation
	st.DB().First(&row, "id=?", pre.BillingID())
	if row.ProviderHeldUnits != 0 || row.State != Reserved {
		t.Fatalf("non-atomic provider admission: %+v", row)
	}
}

func TestProviderExactTargetAndOutputContract(t *testing.T) {
	e, _, user, key := providerBillingFixture(t)
	c := providerTestContract()
	if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"url", "protocol", "model", "channel", "operation", "output_field", "output_cap"} {
		target, limits := providerTestTarget(c), StrictRequest{32, "max_tokens"}
		switch field {
		case "url":
			target.BaseURL = "https://different.invalid"
		case "protocol":
			target.Protocol = "anthropic"
		case "model":
			target.ActualModel = "alias"
		case "channel":
			target.ChannelID++
		case "operation":
			target.Operation = "embeddings"
		case "output_field":
			limits.OutputLimitField = "max_completion_tokens"
		case "output_cap":
			limits.OutputLimit++
		}
		err := e.startDeclaredTestAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: target.ChannelID, ActualModel: target.ActualModel}, target, limits)
		if !errors.Is(err, ErrProviderContract) && !errors.Is(err, ErrProviderLimit) {
			t.Fatalf("%s drift admitted: %v", field, err)
		}
	}
}

func TestProviderEmbeddingBoundCoversWholeBatchAndViolationsStayPending(t *testing.T) {
	e, st, user, key := providerBillingFixture(t)
	c := providerTestContract()
	c.Operation, c.OutputLimitField, c.MaxOutputTokens, c.OutputPricePer1K = "embeddings", "", 0, "0"
	if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	limits, err := ParseStrictRequest([]byte(`{"model":"m","input":["one","two","three"]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.startDeclaredTestAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"}, providerTestTarget(c), limits); err != nil {
		t.Fatal(err)
	}
	var a model.BillingAttempt
	st.DB().First(&a, "reservation_id=?", pre.BillingID())
	var snapshot providerAttemptSnapshot
	if json.Unmarshal([]byte(a.ProviderContractSnapshot), &snapshot) != nil || snapshot.Contract.InputTokenCeiling != 1000 || a.ProviderBoundUnits != 1_000_000 {
		t.Fatalf("lost whole-batch bound: %+v", a)
	}
	u := &relaymodel.Usage{PromptTokens: 1001, TotalTokens: 1001}
	if err := e.EndAttempt(context.Background(), pre, 1, 200, "success", u); !errors.Is(err, ErrProviderLimit) {
		t.Fatalf("contract violation finalized: %v", err)
	}
	_, state, err := e.Finish(context.Background(), pre, nil, "m", "", false)
	if err != nil || state != Pending {
		t.Fatalf("contract violation released user hold: %s %v", state, err)
	}
	st.DB().First(&a, "reservation_id=?", pre.BillingID())
	if a.CostKnown || a.Termination != "provider_contract_violation" {
		t.Fatalf("untrustworthy recovery receipt: %+v", a)
	}
}
