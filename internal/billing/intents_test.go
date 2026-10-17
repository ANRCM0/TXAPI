package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/model"
)

func opaqueIntent(userID, tokenID uint, key string) model.GenerationIntent {
	digest := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	id := digest("intent/" + key)
	now := time.Now()
	return model.GenerationIntent{ID: id, UserID: userID, TokenID: tokenID, AdmissionKey: digest("admission/" + key),
		RequestDigest: digest("opaque-request"), DigestVersion: digest("isolated-digest-key"), Model: "m", Operation: "chat", BillingID: id[:32],
		Ciphertext: bytes.Repeat([]byte{43}, 128), State: model.IntentQueued, CreatedAt: now.UnixMilli(), Deadline: now.Add(time.Second).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
}

func intentLimits() model.GenerationIntentLimits {
	return model.GenerationIntentLimits{InstanceRecords: 32, OwnerRecords: 24, InstanceBytes: 1 << 20, OwnerBytes: 1 << 19}
}

func TestIntentCrossKeyAdmissionCannotOverspendExistingUserBudget(t *testing.T) {
	e, st, user, _ := strictFixture(t, 0.3, 1)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		key := model.Token{UserID: user, Quota: 1}
		if err := st.DB().Create(&key).Error; err != nil {
			t.Fatal(err)
		}
		in := opaqueIntent(user, key.ID, fmt.Sprint(i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.AdmitGenerationIntent(context.Background(), in, intentLimits()); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrQuotaExceeded) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int64
	if err := st.DB().Model(&model.GenerationIntent{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if accepted.Load() != 3 || count != 3 || readBalance(t, st, "user", user).ReservedUnits != 300_000_000 {
		t.Fatalf("cross-Key task admissions overspent: accepted=%d intents=%d", accepted.Load(), count)
	}
}

func TestIntentReservationCannotBeUsedAsAnOrdinaryAttempt(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	in := opaqueIntent(user, key, "no-dispatch")
	stored, err := e.AdmitGenerationIntent(context.Background(), in, intentLimits())
	if err != nil {
		t.Fatal(err)
	}
	pre := &PreDeduct{reservation: &Reservation{id: stored.BillingID}}
	if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1}); !errors.Is(err, ErrStrictUnavailable) {
		t.Fatalf("intent admission became an unguarded dispatch grant: %v", err)
	}
	var attempts int64
	if err := st.DB().Model(&model.BillingAttempt{}).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatalf("attempt escaped: %d %v", attempts, err)
	}
	if readBalance(t, st, "user", user).ReservedUnits != 100_000_000 {
		t.Fatal("refused dispatch lost hold")
	}
}

func TestIntentUnexpectedAttemptRecoveryKeepsPendingHoldsAndAudit(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	stored, err := e.AdmitGenerationIntent(context.Background(), opaqueIntent(user, key, "unexpected-attempt"), intentLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingAttempt{ReservationID: stored.BillingID, Number: 1, State: Active}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id = ?", stored.BillingID).Updates(map[string]any{"state": Active, "provider_held_units": 25}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.RecoverExpired(context.Background(), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var row model.BillingReservation
	if err := st.DB().First(&row, "id = ?", stored.BillingID).Error; err != nil || row.State != Pending || row.ProviderHeldUnits != 25 || row.ChargeUnits != 0 {
		t.Fatalf("uncertain task execution was refunded: %+v %v", row, err)
	}
	var intent model.GenerationIntent
	if err := st.DB().First(&intent, "id = ?", stored.ID).Error; err != nil || intent.State != model.IntentUncertain {
		t.Fatalf("uncertain execution was considered replayable: %+v %v", intent, err)
	}
	for _, owner := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		if b := readBalance(t, st, owner.kind, owner.id); b.ReservedUnits != 100_000_000 || b.UsedUnits != 0 {
			t.Fatalf("lost uncertain hold: %+v", b)
		}
	}
	if got, err := e.CancelGenerationIntent(context.Background(), user, key, stored.ID); err != nil || got.State != model.IntentUncertain {
		t.Fatalf("cancel cleared uncertainty: %+v %v", got, err)
	}
}

func TestIntentCapacityCountsRetainedReceiptsAndCleanupNeverDeletesUnknown(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	limits := intentLimits()
	limits.OwnerRecords = 1
	stored, err := e.AdmitGenerationIntent(context.Background(), opaqueIntent(user, key, "retained"), limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CancelGenerationIntent(context.Background(), user, key, stored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AdmitGenerationIntent(context.Background(), opaqueIntent(user, key, "new"), limits); !errors.Is(err, model.ErrIntentCapacity) {
		t.Fatalf("retained receipt capacity bypassed: %v", err)
	}
	if err := st.DB().Model(&model.GenerationIntent{}).Where("id = ?", stored.ID).Update("expires_at", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := e.CleanupGenerationIntents(context.Background(), 1); err != nil || n != 1 {
		t.Fatalf("safe terminal receipt not cleaned: %d %v", n, err)
	}
	unknown, err := e.AdmitGenerationIntent(context.Background(), opaqueIntent(user, key, "unknown"), limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.GenerationIntent{}).Where("id = ?", unknown.ID).Updates(map[string]any{"state": model.IntentUncertain, "expires_at": time.Now().Add(-time.Second).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := e.CleanupGenerationIntents(context.Background(), 200); err != nil || n != 0 {
		t.Fatalf("cleanup forgot uncertain work: %d %v", n, err)
	}
}
