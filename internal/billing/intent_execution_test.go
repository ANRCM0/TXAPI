package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/store"
)

func TestStrictAttemptReceiptRejectsConflictingReplay(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m", Group: "default"}); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}
	for range 2 {
		if err := e.EndAttempt(ctx, pre, 1, 200, "success", u); err != nil {
			t.Fatal("identical receipt replay failed", err)
		}
	}
	for _, conflicting := range []*relaymodel.Usage{nil, {PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}} {
		if err := e.EndAttempt(ctx, pre, 1, 200, "success", conflicting); !errors.Is(err, ErrSettlementConflict) {
			t.Fatal("conflicting receipt replaced immutable evidence", err)
		}
	}
	if err := e.EndAttempt(ctx, pre, 1, 502, "upstream_error", nil); !errors.Is(err, ErrSettlementConflict) {
		t.Fatal("failed replay replaced successful receipt", err)
	}
	var receipt model.BillingAttempt
	if err := st.DB().First(&receipt, "reservation_id = ? AND number = 1", pre.BillingID()).Error; err != nil || !receipt.CostKnown || receipt.PromptTokens != 2 || receipt.OutputTokens != 1 || receipt.Status != 200 {
		t.Fatal("conflict changed durable receipt", err)
	}
}

func admittedClaimFixture(t *testing.T) (*Engine, *store.Store, model.GenerationIntent) {
	t.Helper()
	e, st, user, key := strictFixture(t, 1, 1)
	in := opaqueIntent(user, key, "claim-fixture")
	in.Deadline = time.Now().Add(20 * time.Second).UnixMilli()
	stored, err := e.AdmitGenerationIntent(context.Background(), in, intentLimits())
	if err != nil {
		t.Fatal(err)
	}
	return e, st, stored
}

func attachClaimOutput(t *testing.T, e *Engine, st *store.Store, in model.GenerationIntent, claim model.GenerationClaim, id string) {
	t.Helper()
	now := time.Now()
	output := model.GenerationRecord{ID: id, UserID: in.UserID, TokenID: in.TokenID, State: "open", MaxBytes: 16384, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	if err := st.CreateGenerationRecord(context.Background(), output, model.GenerationArchiveLimits{InstanceRecords: 10, OwnerRecords: 5, InstanceBytes: 1 << 20, OwnerBytes: 1 << 19}); err != nil {
		t.Fatal(err)
	}
	if err := e.AttachGenerationOutput(context.Background(), claim, id); err != nil {
		t.Fatal(err)
	}
}

func TestIntentClaimConcurrentExclusiveAndExpiredEpochFencesAllWrites(t *testing.T) {
	e, st, in := admittedClaimFixture(t)
	ctx := context.Background()
	claims := make(chan model.GenerationClaim, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, claim, err := e.ClaimGenerationIntent(ctx, 5*time.Second)
			if err == nil {
				claims <- claim
			} else if !errors.Is(err, model.ErrIntentNotFound) {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	close(claims)
	var first model.GenerationClaim
	for claim := range claims {
		if first.ID != "" {
			t.Fatal("concurrent claim created two live owners")
		}
		first = claim
	}
	if first.ID != in.ID || first.Epoch != 1 || len(first.Token) != 64 {
		t.Fatal("invalid first claim")
	}
	attachClaimOutput(t, e, st, in, first, strings.Repeat("a", 64))
	pre, err := e.ResumeGenerationIntent(ctx, first, in.UserID, in.TokenID, in.Operation, in.Model)
	if err != nil || pre.BillingID() != in.BillingID {
		t.Fatal("claim did not adopt original reservation", err)
	}
	if err := st.DB().Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("claim_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.RenewGenerationClaim(ctx, first, time.Second); !errors.Is(err, model.ErrClaimLost) {
		t.Fatal("expired lease was resurrected", err)
	}
	_, second, err := e.ClaimGenerationIntent(ctx, 5*time.Second)
	if err != nil || second.Epoch != 2 || second.Token == first.Token {
		t.Fatal("unstarted work did not receive new epoch", err)
	}
	checks := []struct {
		name  string
		write func() error
	}{
		{"resume", func() error {
			_, err := e.ResumeGenerationIntent(ctx, first, in.UserID, in.TokenID, in.Operation, in.Model)
			return err
		}},
		{"renew", func() error { _, err := e.RenewGenerationClaim(ctx, first, time.Second); return err }},
		{"attach", func() error { return e.AttachGenerationOutput(ctx, first, strings.Repeat("b", 64)) }},
		{"start", func() error { return e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1}) }},
		{"receipt", func() error { return e.EndAttempt(ctx, pre, 1, 200, "success", nil) }},
		{"refusal", func() error { return e.RecordCostRefusal(ctx, pre, ErrProviderContract) }},
		{"finish", func() error { _, _, err := e.Finish(ctx, pre, nil, "", "", false); return err }},
		{"reject", func() error { return e.RejectGenerationClaim(ctx, first) }},
		{"quarantine", func() error { return e.QuarantineGenerationClaim(ctx, first) }},
		{"complete", func() error { return e.CompleteGenerationOutput(ctx, first) }},
	}
	for _, check := range checks {
		if err := check.write(); !errors.Is(err, model.ErrClaimLost) {
			t.Fatalf("stale epoch %s wrote new ownership: %v", check.name, err)
		}
	}
	attachClaimOutput(t, e, st, in, second, strings.Repeat("b", 64))
	current, err := e.ResumeGenerationIntent(ctx, second, in.UserID, in.TokenID, in.Operation, in.Model)
	if err != nil {
		t.Fatal(err)
	}
	if _, state, err := e.Finish(ctx, current, nil, "", "", false); err != nil || state != Settled {
		t.Fatal("current owner could not finish", err)
	}
	if b := readBalance(t, st, "user", in.UserID); b.ReservedUnits != 0 || b.UsedUnits != 0 {
		t.Fatal("fencing corrupted original balance")
	}
}

func TestIntentResumePinsSellingPriceAndRejectsProviderPolicyDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical-selling-price", true: "provider-policy-drift"}[drift], func(t *testing.T) {
			e, st, in := admittedClaimFixture(t)
			ctx := context.Background()
			var original model.BillingReservation
			if err := st.DB().First(&original, "id = ?", in.BillingID).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 1.5, OutputPricePer1K: 6}); err != nil {
				t.Fatal(err)
			}
			if err := e.Prices().ReloadPrices(); err != nil {
				t.Fatal(err)
			}
			_, claim, err := e.ClaimGenerationIntent(ctx, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			attachClaimOutput(t, e, st, in, claim, strings.Repeat("a", 64))
			if drift {
				restarted := New(st.DB(), e.Prices())
				if err := restarted.EnableStrict("0.1", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := restarted.ConfigureProviderCosts("0.1", []ProviderCostContract{providerTestContract()}); err != nil {
					t.Fatal(err)
				}
				e = restarted
			}
			pre, err := e.ResumeGenerationIntent(ctx, claim, in.UserID, in.TokenID, in.Operation, in.Model)
			if drift {
				if !errors.Is(err, ErrProviderContract) || pre != nil {
					t.Fatal("changed provider policy replaced original assumptions", err)
				}
			} else if err != nil || pre.prices.Version() != original.PriceVersion || pre.prices.Version() == e.Prices().Snapshot().Version() {
				t.Fatal("original selling snapshot not retained", err)
			}
			var count int64
			if err := st.DB().Model(&model.BillingReservation{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("resume reserved twice", err)
			}
			if err := e.RejectGenerationClaim(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if b := readBalance(t, st, "key", in.TokenID); b.ReservedUnits != 0 || b.UsedUnits != 0 {
				t.Fatal("unstarted refusal did not safely release original hold")
			}
		})
	}
}

func TestIntentCancellationAndClaimTransitionRollback(t *testing.T) {
	e, st, in := admittedClaimFixture(t)
	ctx := context.Background()
	if err := st.DB().Exec("CREATE TRIGGER reject_claim BEFORE UPDATE OF claim_epoch ON generation_intents BEGIN SELECT RAISE(ABORT, 'controlled claim failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.ClaimGenerationIntent(ctx, time.Second); err == nil {
		t.Fatal("failed claim returned a capability")
	}
	var original model.GenerationIntent
	if err := st.DB().First(&original, "id = ?", in.ID).Error; err != nil || original.ClaimEpoch != 0 || original.State != model.IntentQueued {
		t.Fatal("failed claim leaked ownership", err)
	}
	if err := st.DB().Exec("DROP TRIGGER reject_claim").Error; err != nil {
		t.Fatal(err)
	}
	_, claim, err := e.ClaimGenerationIntent(ctx, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.CancelGenerationIntent(ctx, in.UserID, in.TokenID, in.ID); err != nil || got.State != model.IntentCancelled {
		t.Fatal("claimed/unstarted cancellation failed", err)
	}
	if _, err := e.RenewGenerationClaim(ctx, claim, time.Second); !errors.Is(err, model.ErrClaimLost) {
		t.Fatal("cancelled claim retained ownership", err)
	}
	if b := readBalance(t, st, "user", in.UserID); b.ReservedUnits != 0 {
		t.Fatal("cancel did not release hold")
	}
}

func TestIntentRecoveryLimitSkipsReclaimableUnstartedEpochs(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	ctx := context.Background()
	var rows []model.GenerationIntent
	for _, name := range []string{"unstarted-first", "attempted-second"} {
		in := opaqueIntent(user, key, name)
		in.Deadline = time.Now().Add(20 * time.Second).UnixMilli()
		stored, err := e.AdmitGenerationIntent(ctx, in, intentLimits())
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, stored)
		if err := st.DB().Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"state": model.IntentRunning, "claim_until": time.Now().Add(-time.Second).UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DB().Create(&model.BillingAttempt{ReservationID: rows[1].BillingID, Number: 1, State: Active}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id = ?", rows[1].BillingID).Update("state", Active).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := e.RecoverGenerationIntents(ctx, time.Now(), 1); err != nil || n != 1 {
		t.Fatalf("unstarted row starved attempted recovery: %d %v", n, err)
	}
	var states []model.GenerationIntent
	if err := st.DB().Omit("ciphertext").Order("created_at, id").Find(&states).Error; err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.ID == rows[0].ID && state.State != model.IntentRunning {
			t.Fatal("recovery made unstarted epoch non-reclaimable")
		}
		if state.ID == rows[1].ID && state.State != model.IntentUncertain {
			t.Fatal("attempted work not quarantined")
		}
	}
	if _, claim, err := e.ClaimGenerationIntent(ctx, time.Second); err != nil || claim.ID != rows[0].ID {
		t.Fatal("restart-safe unstarted work was not claimable", err)
	}
}
