package facts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	ownerledger "minigateway/internal/foundation/contracts/ownerledger/v1"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/model"
	"minigateway/internal/ownerbilling"
	"minigateway/internal/store"
)

// Include the compatibility projections and historical prices, not just the
// returned rows: a read must not release holds, settle, or refresh projections.
func ownerFinancialSnapshot(t *testing.T, st *store.Store) []byte {
	t.Helper()
	rows := make(map[string][]map[string]any)
	for _, table := range []string{"billing_balances", "billing_reservations", "billing_attempts", "billing_resolutions", "billing_price_versions", "model_prices", "users", "tokens"} {
		var values []map[string]any
		if err := st.DB().Table(table).Order("rowid").Find(&values).Error; err != nil {
			t.Fatal(err)
		}
		rows[table] = values
	}
	wire, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func assertOwnerFinancialReadOnly(t *testing.T, st *store.Store, read func()) {
	t.Helper()
	before := ownerFinancialSnapshot(t, st)
	read()
	if !bytes.Equal(before, ownerFinancialSnapshot(t, st)) {
		t.Fatal("owner reads changed existing financial authority or compatibility projections")
	}
}

func ownerUnitsEqual(value *string, want string) bool { return value != nil && *value == want }

func TestIndependentOwnerLedgerConsumerUsesRealEngineAndCurrentSessions(t *testing.T) {
	ctx := context.Background()
	database := config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "owner-financial.db")}
	st, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	createOwner := func(name, role string) (*model.User, string, *model.Token) {
		t.Helper()
		user, err := st.CreateUser(&store.UserInput{Username: name, Password: "owner-financial-password", Role: role,
			Quota: 10, ModelMode: access.Allowlist, Models: []string{"m"}})
		if err != nil {
			t.Fatal(err)
		}
		session, _, err := st.Login(name, "owner-financial-password")
		if err != nil {
			t.Fatal(err)
		}
		key, err := st.IssueToken(user.ID, &store.TokenInput{Name: name + "-key", Models: []string{"m"}, Quota: 10})
		if err != nil {
			t.Fatal(err)
		}
		return user, session, key
	}
	owner, session, key := createOwner("financial-owner", model.RoleUser)
	other, otherSession, otherKey := createOwner("financial-other", model.RoleUser)
	admin, adminSession, adminKey := createOwner("financial-admin", model.RoleAdmin)
	newReader := func() ownerledger.Service {
		return ownerbilling.New(st.OwnerLedger(), controlplane.New(persistence.Control(st)), access.NewMemoryLimiter())
	}
	reader := newReader()
	ownerAccess := ownerledger.Access{Session: session}
	otherAccess := ownerledger.Access{Session: otherSession}
	adminAccess := ownerledger.Access{Session: adminSession}
	assertOwnerFinancialReadOnly(t, st, func() {
		unknown, err := reader.Summary(ctx, ownerAccess)
		if err != nil || unknown.BalanceKnown || unknown.PostedUsedUnits != nil || unknown.HeldUnits != nil || unknown.PendingCount != "0" {
			t.Fatal("missing balance must remain unknown", unknown, err)
		}
		wire, err := json.Marshal(unknown)
		if err != nil || !bytes.Contains(wire, []byte(`"posted_used_units":null`)) || !bytes.Contains(wire, []byte(`"held_units":null`)) {
			t.Fatal("unknown balance must serialize as null", string(wire), err)
		}
	})
	// This is an explicit legacy-opening fixture. Every balance, reservation,
	// attempt, charge and decision below is subsequently written by Engine.
	if err := st.DB().Model(&model.User{}).Where("id = ?", owner.ID).Update("used_quota", 0.025).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.15, OutputPricePer1K: 0.6}); err != nil {
		t.Fatal(err)
	}
	prices := billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
	engine := billing.New(st.DB(), prices)
	if err := engine.EnableStrict("0.1", time.Minute); err != nil {
		t.Fatal(err)
	}
	reserve := func(userID, tokenID uint) *billing.PreDeduct {
		t.Helper()
		pre, err := engine.Reserve(ctx, tokenID, userID, "m")
		if err != nil {
			t.Fatal(err)
		}
		return pre
	}
	start := func(pre *billing.PreDeduct) {
		t.Helper()
		if err := engine.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m", Group: "default"}); err != nil {
			t.Fatal(err)
		}
	}
	makePending := func(pre *billing.PreDeduct) {
		t.Helper()
		start(pre)
		if _, state, err := engine.Finish(ctx, pre, nil, "m", "default", false); err != nil || state != billing.Pending {
			t.Fatal("real unknown outcome did not retain pending hold", state, err)
		}
	}
	reserved := reserve(owner.ID, key.ID)
	active := reserve(owner.ID, key.ID)
	start(active)
	pending := reserve(owner.ID, key.ID)
	makePending(pending)
	settled := reserve(owner.ID, key.ID)
	start(settled)
	usage := &execution.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if err := engine.EndAttempt(ctx, settled, 1, 200, "success", usage); err != nil {
		t.Fatal(err)
	}
	charge, state, err := engine.Finish(ctx, settled, usage, "m", "default", true)
	if err != nil || state != billing.Settled || charge != 5_850_000 {
		t.Fatal("real historical-price settlement changed", charge, state, err)
	}
	waived := reserve(owner.ID, key.ID)
	makePending(waived)
	// Expiry is a clock fixture, not a hand-written settlement or released hold.
	if err := st.DB().Model(&model.BillingReservation{}).Where("id IN ?", []string{pending.BillingID(), waived.BillingID()}).Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	decision, err := engine.ResolveReservation(ctx, waived.BillingID(), admin.ID, billing.ResolutionInput{Action: billing.ResolveWaive,
		Reason: "independent consumer fixture", Evidence: "operator reviewed unknown upstream outcome"})
	if err != nil || decision == nil || decision.ChargeUnits != "0" || decision.CostKnown {
		t.Fatal("real waiver must be a known zero user charge without known provider cost", decision, err)
	}
	foreign := reserve(other.ID, otherKey.ID)
	adminOwn := reserve(admin.ID, adminKey.ID)
	oldVersion := settled.PriceVersion()
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 100, OutputPricePer1K: 200}); err != nil {
		t.Fatal(err)
	}
	if err := prices.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	if prices.Snapshot().Version() == oldVersion {
		t.Fatal("fixture failed to publish a different current tariff")
	}
	expected := map[string]string{reserved.BillingID(): billing.Reserved, active.BillingID(): billing.Active, pending.BillingID(): billing.Pending,
		settled.BillingID(): billing.Settled, waived.BillingID(): billing.Settled}
	checkOwner := func() {
		t.Helper()
		summary, err := reader.Summary(ctx, ownerAccess)
		if err != nil || !summary.BalanceKnown || summary.UnitScale != strconv.FormatInt(billing.UnitScale, 10) ||
			!ownerUnitsEqual(summary.PostedUsedUnits, "30850000") || !ownerUnitsEqual(summary.HeldUnits, "300000000") || summary.PendingCount != "1" || summary.AsOf <= 0 {
			t.Fatal("summary mixed opening, live holds and posted charges", summary, err)
		}
		page, err := reader.List(ctx, ownerAccess, ownerledger.Query{Limit: 100})
		if err != nil || len(page.Data) != len(expected) || page.HasMore {
			t.Fatal("owner page lost or included foreign financial rows", page, err)
		}
		for _, row := range page.Data {
			want, exists := expected[row.ID]
			if !exists || row.State != want || row.OriginalReservedUnits != "100000000" || row.PriceVersion != oldVersion || row.TokenID != key.ID || row.OriginModel != "m" {
				t.Fatal("historical reservation changed", row)
			}
			if want == billing.Settled {
				charge := "5850000"
				if row.ID == waived.BillingID() {
					charge = "0"
				}
				if row.HeldUnits != "0" || !ownerUnitsEqual(row.ChargeUnits, charge) {
					t.Fatal("settled zero/charge was confused with original hold", row)
				}
				wire, err := json.Marshal(row)
				if err != nil || !bytes.Contains(wire, []byte(`"charge_units":"`+charge+`"`)) {
					t.Fatal("known historical charge must serialize as an exact decimal string", string(wire), err)
				}
			} else if row.HeldUnits != "100000000" || row.ChargeUnits != nil {
				t.Fatal("unsettled or expired hold invented a known zero charge", row)
			}
			detail, err := reader.Read(ctx, ownerAccess, row.ID)
			if err != nil || detail.Reservation.ID != row.ID || (row.ID == waived.BillingID() && detail.ResolutionAction != billing.ResolveWaive) ||
				(row.ID != waived.BillingID() && detail.ResolutionAction != "") {
				t.Fatal("owner detail lost terminal decision meaning", detail, err)
			}
		}
	}
	assertOwnerFinancialReadOnly(t, st, func() {
		checkOwner()
		page, err := reader.List(ctx, ownerAccess, ownerledger.Query{Limit: 2})
		if err != nil || len(page.Data) != 2 || !page.HasMore {
			t.Fatal("bounded consumer page", page, err)
		}
		for _, filter := range []uint{otherKey.ID, adminKey.ID} {
			page, err := reader.List(ctx, ownerAccess, ownerledger.Query{TokenID: filter, Limit: 100})
			if err != nil || len(page.Data) != 0 {
				t.Fatal("foreign key filter escaped owner scope", page, err)
			}
		}
		for _, attempt := range []struct {
			access ownerledger.Access
			id     string
		}{
			{ownerAccess, foreign.BillingID()}, {otherAccess, settled.BillingID()}, {adminAccess, settled.BillingID()},
		} {
			if _, err := reader.Read(ctx, attempt.access, attempt.id); !errors.Is(err, ownerledger.ErrNotFound) {
				t.Fatal("cross-owner read did not hide existence", err)
			}
		}
		adminPage, err := reader.List(ctx, adminAccess, ownerledger.Query{Limit: 100})
		if err != nil || len(adminPage.Data) != 1 || adminPage.Data[0].ID != adminOwn.BillingID() {
			t.Fatal("admin role widened owner-only capability", adminPage, err)
		}
	})
	if _, err := st.UpdateOwnedToken(owner.ID, key.ID, &store.TokenInput{Name: "revoked creation key", Models: []string{"m"}, Quota: 10, Status: model.TokenStatusDisabled}); err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, checkOwner)
	// Explicitly remove the old archive document after the Engine has committed
	// settlement: owner reads must not consult history to recompute a charge.
	if err := st.DB().Where("version = ?", oldVersion).Delete(&model.BillingPriceVersion{}).Error; err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, checkOwner)
	if err := st.DeleteOwnedToken(owner.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, checkOwner)
	// The retained reader is built with no billing Engine or current price table.
	// Reopening while billing is disabled must still expose committed old charges.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = nil
	st, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	disabled := billing.New(st.DB(), nil)
	if disabled.StrictReady() {
		t.Fatal("disabled billing fixture unexpectedly strict")
	}
	reader = newReader()
	assertOwnerFinancialReadOnly(t, st, checkOwner)
	assertRevoked := func(a ownerledger.Access) {
		t.Helper()
		if _, err := reader.Summary(ctx, a); !errors.Is(err, ownerledger.ErrUnauthorized) {
			t.Fatal("revoked current identity retained summary access", err)
		}
		if _, err := reader.List(ctx, a, ownerledger.Query{Limit: 100}); !errors.Is(err, ownerledger.ErrUnauthorized) {
			t.Fatal("revoked current identity retained list access", err)
		}
		if _, err := reader.Read(ctx, a, settled.BillingID()); !errors.Is(err, ownerledger.ErrUnauthorized) {
			t.Fatal("revoked current identity retained detail access", err)
		}
	}
	if err := st.Logout(session); err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, func() {
		assertRevoked(ownerAccess)
	})
	session, _, err = st.Login(owner.Username, "owner-financial-password")
	if err != nil {
		t.Fatal(err)
	}
	ownerAccess.Session = session
	if _, err := st.UpdateUser(owner.ID, &store.UserInput{Username: owner.Username, Role: model.RoleUser, Status: model.UserStatusDisabled,
		Quota: 10, ModelMode: access.Allowlist, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, func() {
		assertRevoked(ownerAccess)
	})
	// No public user-deletion command exists; this fixture exercises the existing
	// soft-delete state against a still-present, previously valid owner session.
	if err := st.DB().Delete(&model.User{}, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	assertOwnerFinancialReadOnly(t, st, func() { assertRevoked(otherAccess) })
}
