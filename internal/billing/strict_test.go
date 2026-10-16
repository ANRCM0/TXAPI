package billing

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/store"
)

func strictFixture(t *testing.T, userQuota, keyQuota float64) (*Engine, *store.Store, uint, uint) {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "ledger.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	u := model.User{Username: "billing-user", Quota: userQuota}
	if err := st.DB().Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	key := model.Token{UserID: u.ID, Quota: keyQuota}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.15, OutputPricePer1K: 0.6}); err != nil {
		t.Fatal(err)
	}
	table := NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
	e := New(st.DB(), table)
	if err := e.EnableStrict("0.1", time.Minute); err != nil {
		t.Fatal(err)
	}
	return e, st, u.ID, key.ID
}

func readBalance(t *testing.T, st *store.Store, kind string, id uint) model.BillingBalance {
	t.Helper()
	var b model.BillingBalance
	if err := st.DB().First(&b, "owner_type=? AND owner_id=?", kind, id).Error; err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStrictDualBudgetReservationIsAtomic(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 0.01)
	if _, err := e.Reserve(context.Background(), key, user, "m"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err=%v", err)
	}
	var balances, reservations int64
	st.DB().Model(&model.BillingBalance{}).Count(&balances)
	st.DB().Model(&model.BillingReservation{}).Count(&reservations)
	var u model.User
	st.DB().First(&u, user)
	if balances != 0 || reservations != 0 || u.UsedQuota != 0 {
		t.Fatal("key failure left a user hold")
	}
}

func TestStrictInitializationPersistsEnabledMarker(t *testing.T) {
	e, st, _, _ := strictFixture(t, 1, 1)
	if err := st.DB().Model(&model.Setting{}).Where("key=?", StrictEnabledSetting).Update("value", "false").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.EnableStrict("0.1", time.Minute); err != nil {
		t.Fatal(err)
	}
	value, err := st.GetSetting(StrictEnabledSetting)
	if err != nil || value != "true" {
		t.Fatalf("strict ledger initialized without enabled marker: %q %v", value, err)
	}
}

func TestStrictConcurrentKeysCannotOverspendUserBudget(t *testing.T) {
	e, st, user, _ := strictFixture(t, 0.3, 1)
	keys := make([]uint, 30)
	for i := range keys {
		key := model.Token{UserID: user, Quota: 1}
		if err := st.DB().Create(&key).Error; err != nil {
			t.Fatal(err)
		}
		keys[i] = key.ID
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(key uint) {
			defer wg.Done()
			_, err := e.Reserve(context.Background(), key, user, "m")
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrQuotaExceeded) {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	b := readBalance(t, st, "user", user)
	if accepted.Load() != 3 || b.ReservedUnits != 300_000_000 || b.UsedUnits != 0 {
		t.Fatalf("overspend: accepted=%d balance=%+v", accepted.Load(), b)
	}
}

func TestStrictSettlementIsIdempotentAndReturnsRemainder(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ActualModel: "m"}); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if err := e.EndAttempt(context.Background(), pre, 1, 200, "success", u); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cost, state, err := e.Finish(context.Background(), pre, u, "m", "", true)
			if err != nil || state != Settled || cost != 5_850_000 {
				t.Errorf("settlement: %d %s %v", cost, state, err)
			}
		}()
	}
	wg.Wait()
	for _, owner := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		b := readBalance(t, st, owner.kind, owner.id)
		if b.UsedUnits != 5_850_000 || b.ReservedUnits != 0 {
			t.Fatalf("duplicate charge or missing refund: %+v", b)
		}
	}
	if _, _, err := e.Finish(context.Background(), pre, &relaymodel.Usage{PromptTokens: 1000}, "m", "", true); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("conflicting settlement accepted: %v", err)
	}
}

func TestStrictSettlementFailurePreservesBothHolds(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m"}); err != nil {
		t.Fatal(err)
	}
	// Reject the second balance projection, after the user's update ran.
	if err := st.DB().Exec(`CREATE TRIGGER reject_key_projection BEFORE UPDATE OF used_quota ON tokens
		BEGIN SELECT RAISE(ABORT, 'injected key projection failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}
	if _, _, err := e.Finish(ctx, pre, u, "m", "", true); err == nil {
		t.Fatal("injected settlement failure was ignored")
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		b := readBalance(t, st, target.kind, target.id)
		if b.UsedUnits != 0 || b.ReservedUnits != 100_000_000 {
			t.Fatalf("partial settlement escaped transaction: %+v", b)
		}
	}
	var row model.BillingReservation
	if err := st.DB().First(&row, "id=?", pre.BillingID()).Error; err != nil || row.State != Active {
		t.Fatalf("failed settlement advanced receipt: %+v err=%v", row, err)
	}
	if err := st.DB().Exec("DROP TRIGGER reject_key_projection").Error; err != nil {
		t.Fatal(err)
	}
	cost, state, err := e.Finish(ctx, pre, u, "m", "", true)
	if err != nil || cost != 5_850_000 || state != Settled {
		t.Fatalf("retry did not settle once: %d %s %v", cost, state, err)
	}
}

func TestStrictUnknownUsageAndOverCeilingRetainFullHold(t *testing.T) {
	for _, scenario := range []string{"missing", "cancel", "over-ceiling", "invalid-dimensions"} {
		t.Run(scenario, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 1, 1)
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1}); err != nil {
				t.Fatal(err)
			}
			var u *relaymodel.Usage
			if scenario == "over-ceiling" {
				u = &relaymodel.Usage{PromptTokens: 1000000}
			}
			if scenario == "invalid-dimensions" {
				u = &relaymodel.Usage{TotalTokens: 18}
			}
			if scenario == "cancel" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, _, err := e.Finish(ctx, pre, nil, "", "", false); err == nil {
					t.Fatal("canceled write unexpectedly succeeded")
				}
			}
			_, state, err := e.Finish(context.Background(), pre, u, "m", "", true)
			if err != nil || state != Pending {
				t.Fatalf("unknown charge lost: %s %v", state, err)
			}
			b := readBalance(t, st, "user", user)
			if b.UsedUnits != 0 || b.ReservedUnits != 100_000_000 {
				t.Fatalf("unknown execution became free: %+v", b)
			}
		})
	}
}

func TestStrictLeaseRecoveryAndHistoricalReconciliation(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	idle, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	active, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(context.Background(), active, model.BillingAttempt{Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id IN ?", []string{idle.BillingID(), active.BillingID()}).Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	// A new engine has no in-memory reservations. It recovers only durable state.
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 100}); err != nil {
		t.Fatal(err)
	}
	restarted := New(st.DB(), NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := restarted.EnableStrict("0.1", time.Second); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := restarted.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	b := readBalance(t, st, "user", user)
	if b.ReservedUnits != 100_000_000 || b.UsedUnits != 0 {
		t.Fatalf("unsafe recovery: %+v", b)
	}
	if err := restarted.StartAttempt(context.Background(), idle, model.BillingAttempt{Number: 1}); err == nil {
		t.Fatal("recovered lease executed upstream")
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	for range 2 {
		cost, state, err := restarted.ReconcileKnownUsage(context.Background(), active.BillingID(), u, "m", "")
		if err != nil || cost != 5_850_000 || state != Settled {
			t.Fatalf("historical settlement: cost=%d state=%s err=%v", cost, state, err)
		}
	}
	b = readBalance(t, st, "user", user)
	if b.ReservedUnits != 0 || b.UsedUnits != 5_850_000 {
		t.Fatalf("reconciliation duplicated charge: %+v", b)
	}
}

func TestStrictRejectsUnpricedDimensions(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url"}]}],"max_tokens":10}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"tools":[]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"n":2}`,
	} {
		if err := ValidateStrictRequest([]byte(body), false); err == nil {
			t.Fatalf("unpriced request accepted: %s", body)
		}
	}
	if err := ValidateStrictRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`), false); err != nil {
		t.Fatal(err)
	}
}

func TestStrictCrashAfterDurableReceiptAutoSettlesOnce(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ActualModel: "m"}); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if err := e.EndAttempt(context.Background(), pre, 1, 200, "success", u); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id=?", pre.BillingID()).Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	restarted := New(st.DB(), NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := restarted.EnableStrict("0.1", time.Second); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := restarted.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	cost, state, err := restarted.ReconcileKnownUsage(context.Background(), pre.BillingID(), u, "m", "")
	if err != nil || state != Settled || cost != 5_850_000 {
		t.Fatalf("recovery/idempotence failed: %d %s %v", cost, state, err)
	}
	b := readBalance(t, st, "user", user)
	if b.ReservedUnits != 0 || b.UsedUnits != 5_850_000 {
		t.Fatalf("recovery double-charged: %+v", b)
	}
}

func TestStrictRetryCostsAreSeparateFromUserCharge(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ActualModel: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := e.EndAttempt(context.Background(), pre, 1, 502, "upstream_error", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 2, ActualModel: "m"}); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}
	if err := e.EndAttempt(context.Background(), pre, 2, 200, "success", u); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Finish(context.Background(), pre, u, "m", "", true); err != nil {
		t.Fatal(err)
	}
	var attempts []model.BillingAttempt
	if err := st.DB().Where("reservation_id=?", pre.BillingID()).Order("number").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].CostKnown || !attempts[1].CostKnown {
		t.Fatalf("attempt accounting lost uncertainty: %+v", attempts)
	}
	b := readBalance(t, st, "user", user)
	if b.UsedUnits != 5_850_000 || b.ReservedUnits != 0 {
		t.Fatalf("charged attempts instead of final user usage: %+v", b)
	}
}
