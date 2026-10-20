package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"minigateway/internal/billing"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	"minigateway/internal/model"
)

type boundFinanceResource struct {
	*nativeFixtureResource
	endpoint string
	unknown  bool
}

func (r *boundFinanceResource) DialTarget(ctx context.Context, endpoint, key string) (v2.Connection, error) {
	r.endpoint = endpoint
	connection, err := r.Dial(ctx, key)
	if err != nil {
		return nil, err
	}
	if r.unknown {
		return unknownFinanceConnection{connection}, nil
	}
	return connection, nil
}

type unknownFinanceConnection struct{ v2.Connection }

func (unknownFinanceConnection) Read(context.Context) (v2.Frame, error) {
	return v2.Frame{}, io.ErrUnexpectedEOF
}

func strictNativeFixture(t *testing.T, bound, unknown, badCredential bool) (*nativeServiceFixture, *[]*boundFinanceResource) {
	t.Helper()
	f := newNativeServiceFixture(t)
	for _, name := range []string{"alias", "actual"} {
		if err := f.store.UpsertModelPrice(&model.ModelPrice{Model: name, InputPricePer1K: 0.01, OutputPricePer1K: 0.01}); err != nil {
			t.Fatal(err)
		}
	}
	engine := billing.New(f.store.DB(), billing.NewPriceTableWithHistory(f.store.PriceData, time.Hour, f.store.RecordPriceSnapshot))
	if err := engine.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	f.channels[0].BaseURL = "https://compatible-relay.example"
	digestKey := f.channels[0].Credentials[0].APIKey
	if badCredential {
		digestKey = "different-credential"
	}
	hash := sha256.Sum256([]byte(digestKey))
	policy := billing.ProviderCostContract{ChannelID: 1, BaseURL: f.channels[0].BaseURL, Protocol: "openai", ActualModel: "actual", Operation: "responses", OutputLimitField: "max_output_tokens", InputTokenCeiling: 2048, InputBoundProfile: billing.OperatorResponsesV1, MaxOutputTokens: 32, InputPricePer1K: "0.001", OutputPricePer1K: "0.002", Evidence: "fixture all-attempt text bound and additional control fees", TariffProfile: billing.OperatorDeclaredTokensV1, Currency: "USD", CredentialBindings: map[string]string{"7": hex.EncodeToString(hash[:])}, PolicyVersion: "p7-fixture", PolicyExpiresAt: time.Now().Add(time.Hour).Unix(), MaxRequestBytes: 1024, AdditionalCostPerAttempt: "0.0001"}
	if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{policy}); err != nil {
		t.Fatal(err)
	}
	g := f.service.gateway
	g.billingMode, g.billing, g.strictBilling = billing.ModeStrict, engine, engine
	resources := []*boundFinanceResource{}
	if bound {
		prepare := f.service.options.Prepare
		f.service.options.Prepare = func(ctx context.Context) (NativeWSResource, error) {
			resource, err := prepare(ctx)
			if err != nil {
				return nil, err
			}
			wrapped := &boundFinanceResource{nativeFixtureResource: resource.(*nativeFixtureResource), unknown: unknown}
			resources = append(resources, wrapped)
			return wrapped, nil
		}
	}
	f.publish(t)
	return f, &resources
}

func TestNativeStrictEachSelfContainedRoundUsesItsOwnDualHoldAndPinnedActualTarget(t *testing.T) {
	f, resources := strictNativeFixture(t, true, false, false)
	session := f.open(t, "strict-two-rounds")
	body := []byte(`{"type":"response.create","model":"alias","input":"hello","max_output_tokens":32,"store":false}`)
	for range 2 {
		result := session.Execute(f.trusted, contract.Create{JSON: body}, nativeDiscard)
		if result.Problem != nil || !result.Facts.ProtocolComplete {
			t.Fatalf("charged round failed: %+v", result)
		}
	}
	if len(*resources) != 1 || (*resources)[0].endpoint != "wss://compatible-relay.example/v1/responses" || (*resources)[0].conn.count() != 2 || (*resources)[0].dials.Load() != 1 {
		t.Fatalf("selected transport not pinned: %+v", *resources)
	}
	var rows []model.BillingReservation
	if err := f.store.DB().Order("id").Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("not per generation: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.State != billing.Settled || row.ChargeUnits != 30_000 || row.ProviderHeldUnits <= 0 {
			t.Fatalf("wrong settlement: %+v", row)
		}
	}
	var attempts []model.BillingAttempt
	if err := f.store.DB().Find(&attempts).Error; err != nil || len(attempts) != 2 {
		t.Fatalf("wrong attempts: %+v %v", attempts, err)
	}
	for _, attempt := range attempts {
		if attempt.Number != 1 || attempt.CredentialID != 7 || attempt.ActualModel != "actual" || !attempt.CostKnown {
			t.Fatalf("wrong target/cost: %+v", attempt)
		}
	}
	// Connection-local continuation is not a self-contained charged request.
	before := len(rows)
	result := session.Execute(f.trusted, contract.Create{JSON: []byte(`{"type":"response.create","model":"alias","input":"again","max_output_tokens":32,"previous_response_id":"resp-2"}`)}, nativeDiscard)
	if result.Problem == nil || (*resources)[0].conn.count() != 2 {
		t.Fatalf("opaque continuation sent: %+v", result)
	}
	var count int64
	f.store.DB().Model(&model.BillingReservation{}).Count(&count)
	if count != int64(before) {
		t.Fatal("unsupported continuation reserved money")
	}
}

func TestNativeStrictUnknownExecutionRetainsBothHolds(t *testing.T) {
	f, _ := strictNativeFixture(t, true, true, false)
	session := f.open(t, "strict-unknown")
	result := session.Execute(f.trusted, contract.Create{JSON: []byte(`{"type":"response.create","model":"alias","input":"hello","max_output_tokens":32,"store":false}`)}, nativeDiscard)
	var row model.BillingReservation
	if err := f.store.DB().First(&row).Error; err != nil || row.State != billing.Pending || row.ChargeUnits != 0 || row.ProviderHeldUnits <= 0 || !result.Facts.UpstreamWriteStarted {
		t.Fatalf("unknown was refunded or charged: %+v %+v %v", row, result, err)
	}
	var balances []model.BillingBalance
	f.store.DB().Find(&balances)
	for _, balance := range balances {
		if balance.ReservedUnits != row.ReservedUnits || balance.UsedUnits != 0 {
			t.Fatalf("unknown hold lost: %+v", balance)
		}
	}
}

func TestNativeStrictUnsupportedTransportOrCredentialRefusesBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bound, bad bool
	}{{"unbound transport", false, false}, {"uncovered credential", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := strictNativeFixture(t, tc.bound, false, tc.bad)
			session := f.open(t, "strict-refusal")
			result := session.Execute(f.trusted, contract.Create{JSON: []byte(`{"type":"response.create","model":"alias","input":"hello","max_output_tokens":32,"store":false}`)}, nativeDiscard)
			if result.Problem == nil || f.resources[0].dials.Load() != 0 || f.resources[0].conn.count() != 0 {
				t.Fatalf("unproven operation sent: %+v", result)
			}
			var attempts int64
			f.store.DB().Model(&model.BillingAttempt{}).Count(&attempts)
			if attempts != 0 {
				t.Fatal("refusal occupied attempt")
			}
			var row model.BillingReservation
			if err := f.store.DB().First(&row).Error; err != nil || row.State != billing.Settled || row.ChargeUnits != 0 || row.ProviderHeldUnits != 0 {
				t.Fatalf("never-sent reservation retained: %+v %v", row, err)
			}
		})
	}
}
