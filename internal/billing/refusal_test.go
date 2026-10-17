package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
)

func TestProviderRefusalSurvivesRecoveryAndRepeatedFinish(t *testing.T) {
	for _, kind := range []string{"contract", "limit", "fee", "budget", "unpriced", "unpriced retry"} {
		t.Run(kind, func(t *testing.T) {
			e, st, user, key := providerBillingFixture(t)
			c := providerTestContract()
			if err := e.ConfigureProviderCosts("0.001064", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			if kind == "fee" {
				if err := e.EnableStrict("0.1", time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			target, request := providerTestTarget(c), StrictRequest{32, "max_tokens"}
			attempt := model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"}
			wantErr, wantReason := ErrProviderContract, "provider_cost_contract_missing"
			wantState := Settled
			switch kind {
			case "contract":
				target.BaseURL = "https://unconfigured.invalid/secret"
			case "limit":
				request.OutputLimit = 33
				wantErr, wantReason = ErrProviderLimit, "provider_cost_limit"
			case "fee":
				wantErr, wantReason = ErrFeeBound, "billing_fee_limit"
			case "budget", "unpriced retry":
				if err := e.StartCostedAttempt(context.Background(), pre, attempt, target, request); err != nil {
					t.Fatal(err)
				}
				attempt.Number = 2
				wantErr, wantReason = ErrProviderBudget, "provider_budget_exhausted"
				wantState = Pending
			}
			if kind == "unpriced" || kind == "unpriced retry" {
				wantErr, wantReason = ErrUnpricedDimension, "unsupported_billing_dimension"
				err = e.RecordCostRefusal(context.Background(), pre, wantErr)
			} else {
				err = e.StartCostedAttempt(context.Background(), pre, attempt, target, request)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("refusal=%v want=%v", err, wantErr)
			}
			detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
			if err != nil || detail.Reservation.Reason != wantReason {
				t.Fatalf("refusal not durable before finish: %+v %v", detail, err)
			}
			// Simulate losing the request after admission refuses but before finish.
			restarted := New(st.DB(), e.prices)
			if err := restarted.EnableStrict("0.2", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverExpired(context.Background(), time.Now().Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if wantState == Pending {
				wantReason = "execution_or_usage_unknown:" + wantReason
			}
			for i := 0; i < 2; i++ {
				if _, _, err := restarted.Finish(context.Background(), pre, nil, "", "", false); err != nil {
					t.Fatal(err)
				}
			}
			detail, err = restarted.ReservationDetail(context.Background(), pre.BillingID())
			if err != nil || detail.Reservation.State != wantState || detail.Reservation.Reason != wantReason || detail.Reservation.ChargeUnits != "0" {
				t.Fatalf("recovery changed refusal/accounting: %+v %v", detail, err)
			}
			wantAttempts, wantHeld := 0, "0"
			wantReserved := int64(0)
			if wantState == Pending {
				wantAttempts, wantHeld, wantReserved = 1, "1064000", 200_000_000
			}
			if len(detail.Attempts) != wantAttempts || detail.Reservation.ProviderHeldUnits != wantHeld {
				t.Fatalf("refusal created execution or released provider budget: %+v", detail)
			}
			for _, owner := range []struct {
				kind string
				id   uint
			}{{"user", user}, {"key", key}} {
				balance := readBalance(t, st, owner.kind, owner.id)
				if balance.ReservedUnits != wantReserved || balance.UsedUnits != 0 {
					t.Fatalf("refusal changed balance: %+v", balance)
				}
			}
		})
	}
}

func TestProviderRefusalDiagnosticWriteFailureFailsClosed(t *testing.T) {
	e, st, user, key := providerBillingFixture(t)
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Callback().Update().Before("gorm:update").Register("refusal-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "billing_reservations" {
			tx.AddError(errors.New("controlled diagnostic failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB().Callback().Update().Remove("refusal-fault") })
	err = e.StartCostedAttempt(context.Background(), pre, model.BillingAttempt{}, ProviderTarget{}, StrictRequest{})
	if !errors.Is(err, ErrStrictUnavailable) {
		t.Fatalf("diagnostic failure was hidden: %v", err)
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil || len(detail.Attempts) != 0 || detail.Reservation.ProviderHeldUnits != "0" || detail.Reservation.State != Reserved {
		t.Fatalf("failed diagnostic changed execution: %+v %v", detail, err)
	}
}

func TestProviderRefusalRecordsAfterClientCancellation(t *testing.T) {
	e, _, user, key := providerBillingFixture(t)
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.StartCostedAttempt(ctx, pre, model.BillingAttempt{}, ProviderTarget{}, StrictRequest{}); !errors.Is(err, ErrProviderContract) {
		t.Fatal(err)
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil || detail.Reservation.Reason != "provider_cost_contract_missing" || len(detail.Attempts) != 0 {
		t.Fatalf("client cancellation lost diagnostic: %+v %v", detail, err)
	}
}
