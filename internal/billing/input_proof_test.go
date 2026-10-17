package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"minigateway/internal/model"
)

func TestInputProofRequiredBeforeAnyPublicAttempt(t *testing.T) {
	for _, oldAttempt := range []bool{false, true} {
		t.Run(fmt.Sprintf("earlier_unknown=%v", oldAttempt), func(t *testing.T) {
			e, st, user, key := providerBillingFixture(t)
			c := providerTestContract()
			if err := e.ConfigureProviderCosts("0.1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			pre, err := e.Reserve(ctx, key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			attempt := model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: "m"}
			request := StrictRequest{32, "max_tokens"}
			if oldAttempt {
				// A pre-upgrade unknown attempt remains financial authority.
				if err := e.startDeclaredTestAttempt(ctx, pre, attempt, providerTestTarget(c), request); err != nil {
					t.Fatal(err)
				}
				attempt.Number++
			}
			for _, prepared := range []bool{false, true} {
				if prepared {
					err = e.StartPreparedAttempt(ctx, pre, attempt, providerTestTarget(c), request,
						c.BaseURL+"/v1/chat/completions", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32}`), nil)
				} else {
					err = e.StartCostedAttempt(ctx, pre, attempt, providerTestTarget(c), request)
				}
				if !errors.Is(err, ErrInputUnproven) {
					t.Fatalf("attestation admitted execution: %v", err)
				}
			}
			// Crash/restart after refusal must retain the new diagnostic and holds.
			restarted := New(st.DB(), e.prices)
			if err := restarted.EnableStrict("0.2", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverExpired(ctx, time.Now().Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			detail, err := restarted.ReservationDetail(ctx, pre.BillingID())
			if err != nil {
				t.Fatal(err)
			}
			count, held, state, reason, reserved := 0, "0", Settled, "provider_input_unproven", int64(0)
			if oldAttempt {
				count, held, state, reason, reserved = 1, "1064000", Pending, "execution_or_usage_unknown:provider_input_unproven", 200_000_000
			}
			if len(detail.Attempts) != count || detail.Reservation.ProviderHeldUnits != held ||
				detail.Reservation.State != state || detail.Reservation.Reason != reason || detail.Reservation.ChargeUnits != "0" {
				t.Fatalf("refusal changed prior financial facts: %+v", detail)
			}
			for _, owner := range []struct {
				kind string
				id   uint
			}{{"user", user}, {"key", key}} {
				balance := readBalance(t, st, owner.kind, owner.id)
				if balance.ReservedUnits != reserved || balance.UsedUnits != 0 {
					t.Fatalf("incorrect refusal balance: %+v", balance)
				}
			}
		})
	}
}

func TestInputProofMatchesChargedLimitToActualWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire int
		meta int
		want error
	}{
		{"matching", 32, 32, nil},
		{"underbooked", 32, 1, ErrProviderLimit},
		{"different limit", 1, 32, ErrProviderLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, user, key := strictFixture(t, 100, 100)
			if err := e.EnableStrict("20", time.Minute); err != nil {
				t.Fatal(err)
			}
			c := ProviderCostContract{ChannelID: 1, BaseURL: "https://api.openai.com", Protocol: "openai",
				ActualModel: "gpt-4o-mini-2024-07-18", Operation: "chat", InputTokenCeiling: 128000,
				InputBoundProfile: OpenAIChatV1, OutputLimitField: "max_completion_tokens", MaxOutputTokens: 32,
				InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006", Evidence: "isolated tariff fixture"}
			c = withTestTariff(c)
			if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			pre, err := e.Reserve(ctx, key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			attempt := model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: c.ActualModel}
			request := StrictRequest{tc.meta, "max_completion_tokens"}
			body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"service_tier":"default","max_completion_tokens":%d}`, c.ActualModel, tc.wire))
			err = e.StartPreparedAttempt(ctx, pre, attempt, providerTestTarget(c), request, c.BaseURL+"/v1/chat/completions", body, tariffTestHeader())
			if !errors.Is(err, tc.want) {
				t.Fatalf("wire limit admission: %v, want %v", err, tc.want)
			}
			detail, err := e.ReservationDetail(ctx, pre.BillingID())
			if err != nil {
				t.Fatal(err)
			}
			wantAttempts, wantHeld := 0, "0"
			if tc.want == nil {
				wantAttempts, wantHeld = 1, "19219200"
			}
			if len(detail.Attempts) != wantAttempts || detail.Reservation.ProviderHeldUnits != wantHeld {
				t.Fatalf("wire mismatch consumed an envelope: %+v", detail)
			}
		})
	}
}
