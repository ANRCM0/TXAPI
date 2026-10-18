package billing

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func billingAdmin(t *testing.T, st *store.Store) uint {
	t.Helper()
	admin := model.User{Username: "ledger-admin", Role: model.RoleAdmin}
	if err := st.DB().Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	return admin.ID
}

func pendingHold(t *testing.T, e *Engine, st *store.Store, user, key uint) *PreDeduct {
	t.Helper()
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m", Group: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, state, err := e.Finish(ctx, pre, nil, "m", "default", false); err != nil || state != Pending {
		t.Fatalf("pending hold: %s %v", state, err)
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id=?", pre.BillingID()).
		Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	return pre
}

func resolutionUsage(prompt, output int) *ResolutionUsage {
	return &ResolutionUsage{PromptTokens: &prompt, OutputTokens: &output}
}

func TestAdminResolutionUsesHistoryAndCommitsOneAuditedDecision(t *testing.T) {
	for _, action := range []string{ResolveUsage, ResolveCap, ResolveWaive} {
		t.Run(action, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 1, 1)
			admin := billingAdmin(t, st)
			pre := pendingHold(t, e, st, user, key)
			// Today's tariff must not change a historical decision.
			if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 100}); err != nil {
				t.Fatal(err)
			}
			if err := e.Prices().ReloadPrices(); err != nil {
				t.Fatal(err)
			}
			in := ResolutionInput{Action: action, Reason: "verified upstream receipt", Evidence: "provider reference case-123"}
			charge := int64(0)
			switch action {
			case ResolveUsage:
				in.Usage, charge = resolutionUsage(11, 7), 5_850_000
			case ResolveCap:
				in.Usage, charge = resolutionUsage(1000, 0), 100_000_000
			}
			var wg sync.WaitGroup
			for range 10 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, in)
					if err != nil || got == nil || got.ChargeUnits != strconv.FormatInt(charge, 10) || got.ActorID != admin {
						t.Errorf("resolution: %+v %v", got, err)
					}
				}()
			}
			wg.Wait()
			var count int64
			if err := st.DB().Model(&model.BillingResolution{}).Where("reservation_id=?", pre.BillingID()).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("decision duplicated: %d %v", count, err)
			}
			for _, target := range []struct {
				kind string
				id   uint
			}{{"user", user}, {"key", key}} {
				b := readBalance(t, st, target.kind, target.id)
				if b.UsedUnits != charge || b.ReservedUnits != 0 {
					t.Fatalf("incorrect decision accounting: %+v", b)
				}
			}
			detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
			if err != nil || detail.Resolution == nil || detail.Reservation.CanResolve ||
				detail.Reservation.State != Settled || detail.Reservation.PriceVersion != pre.PriceVersion() {
				t.Fatalf("decision detail: %+v %v", detail, err)
			}
			if action == ResolveCap && detail.Resolution.CalculatedUnits != "150000000" {
				t.Fatal("cap decision hid verified excess cost")
			}
			if action == ResolveWaive && detail.Resolution.CostKnown {
				t.Fatal("waiver pretended execution was free")
			}
			in.Evidence = "conflicting evidence"
			if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, in); !errors.Is(err, ErrSettlementConflict) {
				t.Fatalf("decision overwritten: %v", err)
			}
			// A late successful callback cannot charge again after an operator decision.
			_, _, _ = e.Finish(context.Background(), pre, &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}, "m", "default", true)
			if b := readBalance(t, st, "user", user); b.UsedUnits != charge || b.ReservedUnits != 0 {
				t.Fatalf("late callback changed decision: %+v", b)
			}
		})
	}
}

func TestResolutionRequiresExpiredHoldValidEvidenceAndAdministrator(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	admin := billingAdmin(t, st)
	pre := pendingHold(t, e, st, user, key)
	in := ResolutionInput{Action: ResolveUsage, Reason: "verified", Evidence: "receipt", Usage: resolutionUsage(1000, 0)}
	if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), user, in); !errors.Is(err, ErrBillingForbidden) {
		t.Fatalf("ordinary user could settle: %v", err)
	}
	if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, in); !errors.Is(err, ErrCostExceedsHold) {
		t.Fatalf("excess cost silently capped: %v", err)
	}
	for _, bad := range []ResolutionInput{
		{Action: ResolveWaive, Reason: " ", Evidence: "receipt"},
		{Action: ResolveWaive, Reason: "verified", Evidence: ""},
		{Action: ResolveWaive, Reason: "verified", Evidence: "receipt", Usage: resolutionUsage(0, 0)},
		{Action: ResolveUsage, Reason: "verified", Evidence: "receipt"},
		{Action: ResolveUsage, Reason: "verified", Evidence: "receipt", Usage: resolutionUsage(-1, 0)},
		{Action: ResolveCap, Reason: "verified", Evidence: "receipt", Usage: resolutionUsage(1, 0)},
	} {
		if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, bad); !errors.Is(err, ErrLedgerInput) {
			t.Fatalf("invalid resolution accepted: %+v %v", bad, err)
		}
	}
	if err := st.DB().Model(&model.BillingReservation{}).Where("id=?", pre.BillingID()).
		Update("lease_until", time.Now().Add(time.Minute).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, in); !errors.Is(err, ErrNotResolvable) {
		t.Fatalf("live lease resolved: %v", err)
	}
	if b := readBalance(t, st, "user", user); b.UsedUnits != 0 || b.ReservedUnits != 100_000_000 {
		t.Fatal("invalid decision changed budget")
	}
}

func TestResolutionAuditFailureKeepsBalancesAndReceiptUnchanged(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	admin := billingAdmin(t, st)
	pre := pendingHold(t, e, st, user, key)
	if err := st.DB().Exec(`CREATE TRIGGER reject_resolution BEFORE INSERT ON billing_resolutions
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	in := ResolutionInput{Action: ResolveWaive, Reason: "waive uncertain cost", Evidence: "approved case-123"}
	if _, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin, in); err == nil {
		t.Fatal("audit failure ignored")
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		if b := readBalance(t, st, target.kind, target.id); b.UsedUnits != 0 || b.ReservedUnits != 100_000_000 {
			t.Fatalf("audit failure released hold: %+v", b)
		}
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil || detail.Reservation.State != Pending || detail.Resolution != nil {
		t.Fatalf("audit failure advanced decision: %+v %v", detail, err)
	}
}

func TestResolutionAndLateSettlementCannotBothCharge(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	admin := billingAdmin(t, st)
	pre := pendingHold(t, e, st, user, key)
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	go func() {
		<-start
		_, err := e.ResolveReservation(context.Background(), pre.BillingID(), admin,
			ResolutionInput{Action: ResolveWaive, Reason: "accept uncertain cost", Evidence: "approved case-123"})
		errorsSeen <- err
	}()
	go func() {
		<-start
		_, _, err := e.Finish(context.Background(), pre,
			&relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}, "m", "default", true)
		errorsSeen <- err
	}()
	close(start)
	success := 0
	for range 2 {
		err := <-errorsSeen
		if err == nil {
			success++
		} else if !errors.Is(err, ErrNotResolvable) && !errors.Is(err, ErrSettlementConflict) {
			t.Fatalf("unexpected race result: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("expected exactly one terminal decision, got %d", success)
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil {
		t.Fatal(err)
	}
	charge := int64(5_850_000)
	if detail.Resolution != nil {
		charge = 0
		if detail.Resolution.Action != ResolveWaive {
			t.Fatalf("unexpected winning decision: %+v", detail.Resolution)
		}
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", user}, {"key", key}} {
		if b := readBalance(t, st, target.kind, target.id); b.UsedUnits != charge || b.ReservedUnits != 0 {
			t.Fatalf("concurrent finalization broke balance: %+v", b)
		}
	}
}
