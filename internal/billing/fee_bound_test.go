package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
)

func TestFeeBoundAdmissionUsesSellingPriceAndExactHold(t *testing.T) {
	for _, tc := range []struct {
		name, ceiling string
		wantError     bool
	}{
		{"one unit below", "0.169199999", true},
		{"exact", "0.1692", false},
		{"one unit above", "0.169200001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 1, 1)
			if err := e.EnableStrict(tc.ceiling, time.Minute); err != nil {
				t.Fatal(err)
			}
			c := providerTestContract()
			if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			err = e.StartCostedAttempt(context.Background(), pre,
				model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"},
				providerTestTarget(c), StrictRequest{32, "max_tokens"})
			if tc.wantError {
				if !errors.Is(err, ErrFeeBound) {
					t.Fatalf("fee admission did not reject: %v", err)
				}
				var row model.BillingReservation
				if err := st.DB().First(&row, "id=?", pre.BillingID()).Error; err != nil {
					t.Fatal(err)
				}
				var attempts int64
				st.DB().Model(&model.BillingAttempt{}).Where("reservation_id=?", row.ID).Count(&attempts)
				if attempts != 0 || row.ProviderHeldUnits != 0 || row.State != Reserved {
					t.Fatalf("local fee refusal consumed an attempt: row=%+v attempts=%d", row, attempts)
				}
				charge, state, err := e.Finish(context.Background(), pre, nil, "", "", false)
				if err != nil || charge != 0 || state != Settled {
					t.Fatalf("unexecuted fee refusal was charged: %d %s %v", charge, state, err)
				}
				for _, owner := range []struct {
					kind string
					id   uint
				}{{"user", user}, {"key", key}} {
					b := readBalance(t, st, owner.kind, owner.id)
					if b.ReservedUnits != 0 || b.UsedUnits != 0 {
						t.Fatalf("fee refusal leaked a hold: %+v", b)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var a model.BillingAttempt
			if err := st.DB().First(&a, "reservation_id=?", pre.BillingID()).Error; err != nil {
				t.Fatal(err)
			}
			var snapshot providerAttemptSnapshot
			if err := json.Unmarshal([]byte(a.ProviderContractSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Format != 2 || snapshot.SellingBoundUnits != "169200000" ||
				snapshot.SellingPriceVersion != pre.PriceVersion() || snapshot.SellingModel != "m" || a.ProviderBoundUnits != 1_064_000 {
				t.Fatalf("selling/provider guarantees were conflated: %+v %+v", snapshot, a)
			}
		})
	}
}

func TestFeeBoundSelectsActualModelAndPinnedGroupRate(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "actual", InputPricePer1K: 0.01, OutputPricePer1K: 0.02}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupRates(map[string]float64{"relay": 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.prices.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	c := providerTestContract()
	c.ActualModel = "actual"
	if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := pre.PriceVersion()
	// The published tariff and multiplier now make a fresh request too dear.
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "actual", InputPricePer1K: 1, OutputPricePer1K: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupRates(map[string]float64{"relay": 3}); err != nil {
		t.Fatal(err)
	}
	if err := e.prices.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	attempt := model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "actual", Group: "relay"}
	limits := StrictRequest{32, "max_tokens"}
	if err := e.StartCostedAttempt(context.Background(), pre, attempt, providerTestTarget(c), limits); err != nil {
		t.Fatal("old price/rate snapshot was not honored", err)
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot providerAttemptSnapshot
	if err := json.Unmarshal([]byte(detail.Attempts[0].ProviderContractSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SellingBoundUnits != "21280000" || snapshot.SellingPriceVersion != oldVersion ||
		snapshot.SellingModel != "actual" || snapshot.SellingGroup != "relay" {
		t.Fatalf("mapping or multiplier was lost: %+v", snapshot)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}
	if err := e.EndAttempt(context.Background(), pre, 1, 200, "success", u); err != nil {
		t.Fatal(err)
	}
	charge, state, err := e.Finish(context.Background(), pre, u, "actual", "relay", true)
	if err != nil || state != Settled || charge != 500_000 {
		t.Fatalf("settlement diverged from admission snapshot: %d %s %v", charge, state, err)
	}
	fresh, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartCostedAttempt(context.Background(), fresh, attempt, providerTestTarget(c), limits); !errors.Is(err, ErrFeeBound) {
		t.Fatalf("fresh expensive request was admitted: %v", err)
	}
}

func TestFeeBoundDoesNotUseLaterCeilingOrCheaperPublication(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	c := providerTestContract()
	c.ActualModel = "unpriced-actual"
	if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	// Neither lowering the applied price nor raising the engine ceiling can
	// retroactively enlarge this reservation's 0.1 hold or reprice its snapshot.
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := e.prices.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableStrict("0.2", time.Minute); err != nil {
		t.Fatal(err)
	}
	err = e.StartCostedAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: c.ActualModel},
		providerTestTarget(c), StrictRequest{32, "max_tokens"})
	if !errors.Is(err, ErrFeeBound) {
		t.Fatalf("old hold or original-model price fallback was bypassed: %v", err)
	}
}

func TestFeeBoundRoundingZeroAndWideInputCounts(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		input                   int64
		inputPrice, outputPrice float64
		ceiling                 string
		wantError               bool
	}{
		{"single aggregate ceil", 1, 0.000000001, 0.000000001, "0.000000001", false},
		{"zero selling price", math.MaxInt64, 0, 0, "0.000000001", false},
		{"selling overflow", math.MaxInt64, 1, 0, "0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 1, 1)
			if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: tc.inputPrice, OutputPricePer1K: tc.outputPrice}); err != nil {
				t.Fatal(err)
			}
			if err := e.prices.ReloadPrices(); err != nil {
				t.Fatal(err)
			}
			if err := e.EnableStrict(tc.ceiling, time.Minute); err != nil {
				t.Fatal(err)
			}
			c := providerTestContract()
			c.InputTokenCeiling, c.InputPricePer1K, c.OutputPricePer1K = tc.input, "0", "0"
			if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			err = e.StartCostedAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"},
				providerTestTarget(c), StrictRequest{1, "max_tokens"})
			if (tc.wantError && !errors.Is(err, ErrFeeBound)) || (!tc.wantError && err != nil) {
				t.Fatalf("unsafe wide/rounded bound result: %v", err)
			}
		})
	}
}

func TestFeeBoundSnapshotFormatsRemainReadableForReceipts(t *testing.T) {
	for _, format := range []int{1, 2, 3} {
		t.Run(string(rune('0'+format)), func(t *testing.T) {
			e, st, user, key := providerBillingFixture(t)
			c := providerTestContract()
			if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.StartCostedAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"},
				providerTestTarget(c), StrictRequest{32, "max_tokens"}); err != nil {
				t.Fatal(err)
			}
			snapshot := providerAttemptSnapshot{Format: format, UnitScale: UnitScale, Contract: c, OutputLimit: 32}
			if format >= 2 {
				snapshot.SellingPriceVersion, snapshot.SellingModel, snapshot.SellingBoundUnits = pre.PriceVersion(), "m", "169200000"
			}
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(payload)
			if err := st.DB().Model(&model.BillingAttempt{}).Where("reservation_id=?", pre.BillingID()).Updates(map[string]any{
				"provider_contract_snapshot": string(payload), "provider_contract_version": hex.EncodeToString(hash[:]),
			}).Error; err != nil {
				t.Fatal(err)
			}
			err = e.EndAttempt(context.Background(), pre, 1, 200, "success", &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7})
			if (format == 3 && !errors.Is(err, ErrProviderLimit)) || (format != 3 && err != nil) {
				t.Fatalf("format %d receipt handling: %v", format, err)
			}
		})
	}
}
