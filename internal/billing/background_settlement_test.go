package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

func TestBackgroundReservationRestorePinsPolicyIdentityDeadlineAndUnstartedHold(t *testing.T) {
	for _, change := range []string{"identity", "model", "expired", "policy", "attempt", "settled", "price_tamper"} {
		t.Run(change, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 10, 10)
			c := operatorContract()
			if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			id, _, err := e.ReserveBackground(context.Background(), key, user, "m", []byte(operatorRequest))
			if err != nil {
				t.Fatal(err)
			}
			useKey, name := key, "m"
			switch change {
			case "identity":
				useKey++
			case "model":
				name = "other"
			case "expired":
				st.DB().Model(&model.BillingReservation{}).Where("id = ?", id).Update("lease_until", time.Now().Add(-time.Second).UnixMilli())
			case "policy":
				st.DB().Model(&model.BillingReservation{}).Where("id = ?", id).Update("provider_policy_version", strings.Repeat("0", 64))
			case "attempt":
				st.DB().Create(&model.BillingAttempt{ReservationID: id, Number: 1, ActualModel: "m", State: Active})
			case "settled":
				if err := e.CompleteBackgroundReservation(context.Background(), id, key, user, "m", nil, false); err != nil {
					t.Fatal(err)
				}
			case "price_tamper":
				st.DB().Model(&model.BillingPriceVersion{}).Where("1 = 1").Update("payload", `{}`)
			}
			if _, err := e.ResumeReservation(context.Background(), id, useKey, user, name, true); err == nil {
				t.Fatal("unsafe background reservation resumed")
			}
		})
	}
}

func TestBackgroundUnknownHoldAcceptsLateKnownUsageExactlyOnceAfterRecovery(t *testing.T) {
	ctx := context.Background()
	e, st, user, key := strictFixture(t, 10, 10)
	if err := e.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	c := operatorContract()
	if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	id, _, err := e.ReserveBackground(ctx, key, user, "m", []byte(operatorRequest))
	if err != nil {
		t.Fatal(err)
	}
	pre, err := e.ResumeReservation(ctx, id, key, user, "m", true)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := ParseResponsesStrictRequest([]byte(operatorRequest))
	attempt := model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: "m"}
	if err := e.StartPreparedAttempt(ctx, pre, attempt, providerTestTarget(c), request, c.BaseURL+"/v1/responses", []byte(operatorRequest), tariffTestHeader()); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteBackgroundReservation(ctx, id, key, user, "m", nil, false); err != nil {
		t.Fatal(err)
	}
	var row model.BillingReservation
	var receipt model.BillingAttempt
	if err := st.DB().First(&row, "id = ?", id).Error; err != nil || row.State != Pending || row.ChargeUnits != 0 || row.ProviderHeldUnits == 0 {
		t.Fatalf("unknown hold lost: %+v %v", row, err)
	}
	if err := st.DB().First(&receipt, "reservation_id = ?", id).Error; err != nil || receipt.State != Active {
		t.Fatalf("unknown receipt was prematurely ended: %+v %v", receipt, err)
	}
	if err := e.RecoverExpired(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Current selling prices and policy changes cannot rewrite the old receipt.
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 9, OutputPricePer1K: 9}); err != nil {
		t.Fatal(err)
	}
	restarted := New(st.DB(), NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := restarted.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	c.PolicyVersion = "changed-policy"
	if err := restarted.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	usage := &v1.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}
	for range 2 {
		if err := restarted.CompleteBackgroundReservation(ctx, id, key, user, "m", usage, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DB().First(&row, "id = ?", id).Error; err != nil || row.State != Settled || row.ChargeUnits != 900_000 {
		t.Fatalf("late receipt did not settle old price exactly once: %+v %v", row, err)
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		balance := readBalance(t, st, target.kind, target.id)
		if balance.UsedUnits != 900_000 || balance.ReservedUnits != 0 {
			t.Fatalf("balance %+v", balance)
		}
	}
	changed := &v1.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}
	if err := restarted.CompleteBackgroundReservation(ctx, id, key, user, "m", changed, true); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("changed receipt accepted: %v", err)
	}
}

func TestBackgroundAdmissionLostAckRetainsLinkedHoldButOrphanCanRelease(t *testing.T) {
	e, st, user, key := strictFixture(t, 10, 10)
	if err := e.ConfigureProviderCosts("1", []ProviderCostContract{operatorContract()}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, _, err := e.ReserveBackground(ctx, key, user, "m", []byte(operatorRequest))
	if err != nil {
		t.Fatal(err)
	}
	task := model.BackgroundJob{ID: "task", PublicID: "task-public", UserID: user, TokenID: key, Model: "m", KeyHash: "task-key", BillingMode: "strict", BillingID: id, Stage: "queued", Ciphertext: []byte{}, Snapshot: []byte{}}
	if err := st.DB().Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteBackgroundReservation(ctx, id, key, user, "m", nil, false); err != nil {
		t.Fatal(err)
	}
	var row model.BillingReservation
	st.DB().First(&row, "id = ?", id)
	if row.State != Reserved {
		t.Fatalf("lost ack refunded live task: %+v", row)
	}
	if err := st.DB().Model(&task).Update("stage", "terminal").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteBackgroundReservation(ctx, id, key, user, "m", nil, false); err != nil {
		t.Fatal(err)
	}
	st.DB().First(&row, "id = ?", id)
	if row.State != Settled || row.ChargeUnits != 0 {
		t.Fatalf("never-sent hold was not released: %+v", row)
	}
}
