package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	ledger "minigateway/internal/foundation/contracts/ledger/v1"
	ownerledger "minigateway/internal/foundation/contracts/ownerledger/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type ownerLedgerFixture struct {
	store                               *store.Store
	path                                string
	owner, other, admin                 model.User
	key, key2, otherKey                 uint
	session, otherSession, adminSession string
}

func newOwnerLedgerFixture(t *testing.T) ownerLedgerFixture {
	t.Helper()
	f := ownerLedgerFixture{path: filepath.Join(t.TempDir(), "owner-ledger.db")}
	var err error
	f.store, err = store.Open(config.DatabaseConfig{Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	for i, name := range []string{"finance-owner", "finance-other", "finance-admin"} {
		role := model.RoleUser
		if i == 2 {
			role = model.RoleAdmin
		}
		u, e := f.store.CreateUser(&store.UserInput{Username: name, Password: "owner-ledger-password-123", Role: role, ModelMode: access.All, Quota: 10})
		if e != nil {
			t.Fatal(e)
		}
		session, _, e := f.store.Login(name, "owner-ledger-password-123")
		if e != nil {
			t.Fatal(e)
		}
		switch i {
		case 0:
			f.owner, f.session = *u, session
		case 1:
			f.other, f.otherSession = *u, session
		case 2:
			f.admin, f.adminSession = *u, session
		}
	}
	for i, owner := range []uint{f.owner.ID, f.owner.ID, f.other.ID} {
		token, e := f.store.CreateToken(&store.TokenInput{Key: fmt.Sprintf("owner-ledger-fixture-key-%d", i), UserID: owner, ModelMode: access.All, Quota: 10})
		if e != nil {
			t.Fatal(e)
		}
		switch i {
		case 0:
			f.key = token.ID
		case 1:
			f.key2 = token.ID
		case 2:
			f.otherKey = token.ID
		}
	}
	return f
}

func (f ownerLedgerFixture) access() ownerledger.Access {
	return ownerledger.Access{Session: f.session}
}

func ownerLedgerReservation(t *testing.T, f ownerLedgerFixture, n int, owner, token uint, state string, reserved, charge int64) model.BillingReservation {
	t.Helper()
	now := time.Now().UTC()
	row := model.BillingReservation{ID: fmt.Sprintf("%032x", n), UserID: owner, TokenID: token, OriginModel: "public-model", PriceVersion: strings.Repeat("a", 64), ReservedUnits: reserved, ChargeUnits: charge, State: state, LeaseUntil: now.Add(-time.Hour).UnixMilli(), CreatedAt: now, UpdatedAt: now}
	if e := f.store.DB().Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	return row
}

func ownerLedgerFinancialSnapshot(t *testing.T, st *store.Store) []byte {
	t.Helper()
	values := make(map[string][]map[string]any)
	for _, table := range []string{"billing_balances", "billing_reservations", "billing_attempts", "billing_resolutions", "billing_price_versions", "model_prices", "settings", "users", "tokens", "schema_migrations"} {
		var rows []map[string]any
		if e := st.DB().Table(table).Find(&rows).Error; e != nil {
			t.Fatal(e)
		}
		values[table] = rows
	}
	encoded, e := json.Marshal(values)
	if e != nil {
		t.Fatal(e)
	}
	return encoded
}

func ownerLedgerEngine(t *testing.T, f ownerLedgerFixture) *billing.Engine {
	t.Helper()
	if e := f.store.UpsertModelPrice(&model.ModelPrice{Model: "public-model", InputPricePer1K: 0.15, OutputPricePer1K: 0.6}); e != nil {
		t.Fatal(e)
	}
	engine := billing.New(f.store.DB(), billing.NewPriceTableWithHistory(f.store.PriceData, time.Hour, f.store.RecordPriceSnapshot))
	if e := engine.EnableStrict("0.1", time.Minute); e != nil {
		t.Fatal(e)
	}
	return engine
}

func ownerLedgerPending(t *testing.T, f ownerLedgerFixture, engine *billing.Engine) *billing.PreDeduct {
	t.Helper()
	pre, e := engine.Reserve(context.Background(), f.key, f.owner.ID, "public-model")
	if e != nil {
		t.Fatal(e)
	}
	if e = engine.StartAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ActualModel: "public-model", Group: "default"}); e != nil {
		t.Fatal(e)
	}
	if _, state, e := engine.Finish(context.Background(), pre, nil, "public-model", "default", false); e != nil || state != ledger.Pending {
		t.Fatal(state, e)
	}
	if e = f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", pre.BillingID()).Update("lease_until", time.Now().Add(-time.Minute).UnixMilli()).Error; e != nil {
		t.Fatal(e)
	}
	return pre
}

func TestOwnerLedgerSummaryDistinguishesMissingZeroAndPreciseUserBalance(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	repository := f.store.OwnerLedger()
	if e := f.store.DB().Model(&model.User{}).Where("id = ?", f.owner.ID).Update("used_quota", 99.25).Error; e != nil {
		t.Fatal(e)
	}
	missing, e := repository.Summary(context.Background(), f.access())
	if e != nil || missing.BalanceKnown || missing.PostedUsedUnits != nil || missing.HeldUnits != nil || missing.PendingCount != "0" || missing.UnitScale != "1000000000" || missing.AsOf == 0 {
		t.Fatal("missing authority was reconstructed", missing, e)
	}
	large := int64(9007199254740993)
	for _, row := range []model.BillingBalance{{OwnerType: "user", OwnerID: f.owner.ID, UsedUnits: large, ReservedUnits: 17}, {OwnerType: "key", OwnerID: f.key, UsedUnits: large, ReservedUnits: 17}, {OwnerType: "key", OwnerID: f.key2, UsedUnits: large, ReservedUnits: 17}, {OwnerType: "user", OwnerID: f.other.ID, UsedUnits: 3, ReservedUnits: 4}} {
		if e := f.store.DB().Create(&row).Error; e != nil {
			t.Fatal(e)
		}
	}
	a := ownerLedgerReservation(t, f, 1, f.owner.ID, f.key, ledger.Pending, 9, 0)
	ownerLedgerReservation(t, f, 2, f.owner.ID, f.key2, ledger.Pending, 8, 0)
	ownerLedgerReservation(t, f, 3, f.other.ID, f.otherKey, ledger.Pending, 4, 0)
	before := ownerLedgerFinancialSnapshot(t, f.store)
	summary, e := repository.Summary(context.Background(), f.access())
	if e != nil || !summary.BalanceKnown || summary.PostedUsedUnits == nil || *summary.PostedUsedUnits != "9007199254740993" || summary.HeldUnits == nil || *summary.HeldUnits != "17" || summary.PendingCount != "2" {
		t.Fatal("user/key amounts double counted or rounded", summary, e)
	}
	detail, e := repository.Read(context.Background(), f.access(), a.ID)
	if e != nil || detail.Reservation.ChargeUnits != nil || detail.Reservation.HeldUnits != "9" || detail.Reservation.LeaseUntil >= time.Now().UnixMilli() {
		t.Fatal("expired pending hold was released or made free", detail, e)
	}
	raw, _ := json.Marshal(summary)
	if !bytes.Contains(raw, []byte(`"posted_used_units":"9007199254740993"`)) {
		t.Fatal(string(raw))
	}
	if !bytes.Equal(before, ownerLedgerFinancialSnapshot(t, f.store)) {
		t.Fatal("owner reads changed financial data or schema")
	}
	if e = f.store.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: f.admin.ID}).Error; e != nil {
		t.Fatal(e)
	}
	zero, e := repository.Summary(context.Background(), ownerledger.Access{Session: f.adminSession})
	if e != nil || !zero.BalanceKnown || zero.PostedUsedUnits == nil || *zero.PostedUsedUnits != "0" || zero.HeldUnits == nil || *zero.HeldUnits != "0" {
		t.Fatal("known zero balance became unknown", zero, e)
	}
}

func TestOwnerLedgerUsesExistingEngineLifecycleAndKeepsHistoricalCharge(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	if e := f.store.DB().Model(&model.User{}).Where("id = ?", f.owner.ID).Update("used_quota", 0.000000019).Error; e != nil {
		t.Fatal(e)
	}
	engine := ownerLedgerEngine(t, f)
	ctx := context.Background()
	reserved, e := engine.Reserve(ctx, f.key, f.owner.ID, "public-model")
	if e != nil {
		t.Fatal(e)
	}
	active, e := engine.Reserve(ctx, f.key2, f.owner.ID, "public-model")
	if e != nil {
		t.Fatal(e)
	}
	if e = engine.StartAttempt(ctx, active, model.BillingAttempt{Number: 1, ActualModel: "public-model", Group: "default"}); e != nil {
		t.Fatal(e)
	}
	pending := ownerLedgerPending(t, f, engine)
	zero, e := engine.Reserve(ctx, f.key2, f.owner.ID, "public-model")
	if e != nil {
		t.Fatal(e)
	}
	if charge, state, e := engine.Finish(ctx, zero, nil, "", "", false); e != nil || charge != 0 || state != ledger.Settled {
		t.Fatal(charge, state, e)
	}
	settled, e := engine.Reserve(ctx, f.key, f.owner.ID, "public-model")
	if e != nil {
		t.Fatal(e)
	}
	if e = engine.StartAttempt(ctx, settled, model.BillingAttempt{Number: 1, ActualModel: "public-model", Group: "default"}); e != nil {
		t.Fatal(e)
	}
	usage := &execution.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if e = engine.EndAttempt(ctx, settled, 1, 200, "success", usage); e != nil {
		t.Fatal(e)
	}
	if charge, state, e := engine.Finish(ctx, settled, usage, "public-model", "default", true); e != nil || charge != 5850000 || state != ledger.Settled {
		t.Fatal(charge, state, e)
	}
	repository := f.store.OwnerLedger()
	before := ownerLedgerFinancialSnapshot(t, f.store)
	summary, e := repository.Summary(ctx, f.access())
	if e != nil || *summary.PostedUsedUnits != "5850019" || *summary.HeldUnits != "300000000" || summary.PendingCount != "1" {
		t.Fatal("opening/charge/live hold mismatch", summary, e)
	}
	for _, tc := range []struct {
		pre                 *billing.PreDeduct
		state, held, charge string
	}{{reserved, ledger.Reserved, "100000000", ""}, {active, ledger.Active, "100000000", ""}, {pending, ledger.Pending, "100000000", ""}, {zero, ledger.Settled, "0", "0"}, {settled, ledger.Settled, "0", "5850000"}} {
		d, e := repository.Read(ctx, f.access(), tc.pre.BillingID())
		if e != nil || d.Reservation.State != tc.state || d.Reservation.HeldUnits != tc.held || d.Reservation.OriginalReservedUnits != "100000000" || d.Reservation.PriceVersion != tc.pre.PriceVersion() {
			t.Fatal(tc, d, e)
		}
		if tc.charge == "" {
			if d.Reservation.ChargeUnits != nil {
				t.Fatal("unknown charge became zero", d)
			}
		} else if d.Reservation.ChargeUnits == nil || *d.Reservation.ChargeUnits != tc.charge {
			t.Fatal("known settled charge lost", d)
		}
	}
	if !bytes.Equal(before, ownerLedgerFinancialSnapshot(t, f.store)) {
		t.Fatal("read capability wrote financial data")
	}
	if e = f.store.UpsertModelPrice(&model.ModelPrice{Model: "public-model", InputPricePer1K: 9, OutputPricePer1K: 9}); e != nil {
		t.Fatal(e)
	}
	if e = engine.Prices().ReloadPrices(); e != nil {
		t.Fatal(e)
	}
	if e = f.store.DB().Where("version = ?", settled.PriceVersion()).Delete(&model.BillingPriceVersion{}).Error; e != nil {
		t.Fatal(e)
	}
	afterPrice := ownerLedgerFinancialSnapshot(t, f.store)
	d, e := repository.Read(ctx, f.access(), settled.BillingID())
	if e != nil || d.Reservation.PriceVersion != settled.PriceVersion() || d.Reservation.ChargeUnits == nil || *d.Reservation.ChargeUnits != "5850000" {
		t.Fatal("owner read repriced charge or required old price payload", d, e)
	}
	if !bytes.Equal(afterPrice, ownerLedgerFinancialSnapshot(t, f.store)) {
		t.Fatal("read recreated historical price or changed financial rows")
	}
}

func TestOwnerLedgerCurrentOwnerAndRevokedCreationKeys(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	row := ownerLedgerReservation(t, f, 10, f.owner.ID, f.key, ledger.Pending, 100, 0)
	repository := f.store.OwnerLedger()
	for _, session := range []string{f.otherSession, f.adminSession} {
		if detail, e := repository.Read(context.Background(), ownerledger.Access{Session: session}, row.ID); e != ownerledger.ErrNotFound || !reflect.DeepEqual(detail, ownerledger.Detail{}) {
			t.Fatal("foreign or admin session read owner row", detail, e)
		}
		page, e := repository.List(context.Background(), ownerledger.Access{Session: session}, ownerledger.Query{})
		if e != nil || page.Data == nil || len(page.Data) != 0 {
			t.Fatal("foreign list exposed record", page, e)
		}
	}
	for _, patch := range []map[string]any{{"status": model.TokenStatusDisabled}, {"expires_at": time.Now().Add(-time.Hour).Unix()}, {"deleted_at": time.Now()}} {
		if e := f.store.DB().Model(&model.Token{}).Where("id = ?", f.key).Updates(patch).Error; e != nil {
			t.Fatal(e)
		}
		detail, e := repository.Read(context.Background(), f.access(), row.ID)
		if e != nil || detail.Reservation.ID != row.ID || detail.Reservation.HeldUnits != "100" {
			t.Fatal("creation key revocation discarded owner history", detail, e)
		}
		page, e := repository.List(context.Background(), f.access(), ownerledger.Query{TokenID: f.key})
		if e != nil || len(page.Data) != 1 || page.Data[0].ID != row.ID {
			t.Fatal("old key filter required active key", page, e)
		}
	}
	for _, session := range []string{"", "owner-ledger-fixture-key-0", "session-invented"} {
		if out, e := repository.Summary(context.Background(), ownerledger.Access{Session: session}); e != ownerledger.ErrUnauthorized || !reflect.DeepEqual(out, ownerledger.Summary{}) {
			t.Fatal("non-session reached ledger", out, e)
		}
	}
}

func TestOwnerLedgerSessionRevocationDisablingAndSoftDeletion(t *testing.T) {
	for _, scenario := range []string{"logout", "expired-session", "disabled", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOwnerLedgerFixture(t)
			row := ownerLedgerReservation(t, f, 11, f.owner.ID, f.key, ledger.Settled, 100, 0)
			switch scenario {
			case "logout":
				if e := f.store.Logout(f.session); e != nil {
					t.Fatal(e)
				}
			case "expired-session":
				if e := f.store.DB().Model(&model.Session{}).Where("hash = ?", access.Hash(f.session)).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error; e != nil {
					t.Fatal(e)
				}
			case "disabled":
				if e := f.store.DB().Model(&model.User{}).Where("id = ?", f.owner.ID).Update("status", model.UserStatusDisabled).Error; e != nil {
					t.Fatal(e)
				}
			case "deleted":
				if e := f.store.DB().Delete(&model.User{}, f.owner.ID).Error; e != nil {
					t.Fatal(e)
				}
			}
			repo := f.store.OwnerLedger()
			if out, e := repo.Summary(context.Background(), f.access()); e != ownerledger.ErrUnauthorized || !reflect.DeepEqual(out, ownerledger.Summary{}) {
				t.Fatal(out, e)
			}
			if out, e := repo.List(context.Background(), f.access(), ownerledger.Query{}); e != ownerledger.ErrUnauthorized || !reflect.DeepEqual(out, ownerledger.Page{}) {
				t.Fatal(out, e)
			}
			if out, e := repo.Read(context.Background(), f.access(), row.ID); e != ownerledger.ErrUnauthorized || !reflect.DeepEqual(out, ownerledger.Detail{}) {
				t.Fatal(out, e)
			}
		})
	}
}

func TestOwnerLedgerPaginationFiltersAndPrivateSQLProjection(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	created := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= 6; i++ {
		key := f.key
		if i%2 == 0 {
			key = f.key2
		}
		state := ledger.Pending
		if i%3 == 0 {
			state = ledger.Settled
		}
		row := ownerLedgerReservation(t, f, 20+i, f.owner.ID, key, state, 100, 0)
		if e := f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", row.ID).Updates(map[string]any{"created_at": created, "reason": "private-reason-sentinel", "provider_budget_units": 99, "provider_held_units": 98}).Error; e != nil {
			t.Fatal(e)
		}
	}
	foreign := ownerLedgerReservation(t, f, 99, f.other.ID, f.otherKey, ledger.Pending, 100, 0)
	decisionID := fmt.Sprintf("%032x", 26)
	if e := f.store.DB().Create(&model.BillingResolution{ReservationID: decisionID, Action: ledger.ResolveWaive, ActorID: f.admin.ID, Reason: "private-reason-sentinel", Evidence: "private-evidence-sentinel", Fingerprint: strings.Repeat("e", 64)}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.store.DB().Create(&model.BillingAttempt{ReservationID: decisionID, Number: 1, ActualModel: "private-actual-sentinel", Group: "private-group-sentinel", ProviderContractSnapshot: "private-contract-sentinel"}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.store.DB().Create(&model.BillingPriceVersion{Version: strings.Repeat("a", 64), Payload: "private-price-payload-sentinel"}).Error; e != nil {
		t.Fatal(e)
	}
	before := ownerLedgerFinancialSnapshot(t, f.store)
	guarding := true
	var statements []string
	hook := "test-owner-ledger-projection"
	if e := f.store.DB().Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if !guarding {
			return
		}
		sql := strings.ToLower(tx.Statement.SQL.String())
		statements = append(statements, sql)
		if _, ok := tx.Statement.Context.Deadline(); !ok {
			t.Error("owner query lacks finite deadline")
		}
		for _, private := range []string{"provider_", "reason", "evidence", "actor_id", "billing_attempts", "billing_price_versions", "payload"} {
			if strings.Contains(sql, private) {
				t.Errorf("owner repository loaded private financial data: %s", sql)
			}
		}
		if strings.Contains(strings.ReplaceAll(sql, "count(*)", "count(1)"), "*") {
			t.Errorf("owner repository used an unbounded SELECT: %s", sql)
		}
	}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.DB().Callback().Query().Remove(hook) })
	repository := f.store.OwnerLedger()
	page, e := repository.List(context.Background(), f.access(), ownerledger.Query{Limit: 2})
	if e != nil || len(page.Data) != 2 || !page.HasMore || page.Data[0].ID != fmt.Sprintf("%032x", 26) || page.Data[1].ID != fmt.Sprintf("%032x", 25) {
		t.Fatal("unstable owner page", page, e)
	}
	page, e = repository.List(context.Background(), f.access(), ownerledger.Query{Limit: 2, Offset: 2})
	if e != nil || len(page.Data) != 2 || page.Data[0].ID != fmt.Sprintf("%032x", 24) || page.Data[1].ID != fmt.Sprintf("%032x", 23) {
		t.Fatal(page, e)
	}
	page, e = repository.List(context.Background(), f.access(), ownerledger.Query{TokenID: f.otherKey})
	if e != nil || page.Data == nil || len(page.Data) != 0 || page.HasMore {
		t.Fatal("foreign token filter exposed rows", page, e)
	}
	page, e = repository.List(context.Background(), f.access(), ownerledger.Query{TokenID: f.key2, State: ledger.Settled})
	if e != nil || len(page.Data) != 1 || page.Data[0].ID != decisionID || page.Data[0].ChargeUnits == nil || *page.Data[0].ChargeUnits != "0" {
		t.Fatal(page, e)
	}
	detail, e := repository.Read(context.Background(), f.access(), decisionID)
	if e != nil || detail.ResolutionAction != ledger.ResolveWaive {
		t.Fatal(detail, e)
	}
	raw, _ := json.Marshal(detail)
	if bytes.Contains(raw, []byte("private-")) {
		t.Fatal("private detail escaped projection", string(raw))
	}
	if _, e = repository.Summary(context.Background(), f.access()); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{foreign.ID, strings.Repeat("f", 32), "not-a-reservation", strings.ToUpper(strings.Repeat("a", 32))} {
		if out, e := repository.Read(context.Background(), f.access(), id); e != ownerledger.ErrNotFound || !reflect.DeepEqual(out, ownerledger.Detail{}) {
			t.Fatal("detail enumeration/error leak", out, e)
		}
	}
	for _, q := range []ownerledger.Query{{State: "new-state"}, {Limit: -1}, {Limit: 101}, {Offset: -1}, {Offset: 10001}} {
		if out, e := repository.List(context.Background(), f.access(), q); e != ownerledger.ErrInvalid || !reflect.DeepEqual(out, ownerledger.Page{}) {
			t.Fatal(out, e)
		}
	}
	guarding = false
	if len(statements) == 0 {
		t.Fatal("SQL projection guard never observed reads")
	}
	if !bytes.Equal(before, ownerLedgerFinancialSnapshot(t, f.store)) {
		t.Fatal("owner queries changed financial/schema data")
	}
}

func TestOwnerLedgerOnlyExposesExistingTerminalResolutionActions(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	engine := ownerLedgerEngine(t, f)
	repository := f.store.OwnerLedger()
	for _, action := range []string{ledger.ResolveUsage, ledger.ResolveCap, ledger.ResolveWaive} {
		pre := ownerLedgerPending(t, f, engine)
		input := billing.ResolutionInput{Action: action, Reason: "private resolution reason", Evidence: "private resolution evidence"}
		want := "0"
		if action != ledger.ResolveWaive {
			prompt, output := 11, 7
			if action == ledger.ResolveCap {
				prompt, output = 1000, 0
				want = "100000000"
			} else {
				want = "5850000"
			}
			input.Usage = &billing.ResolutionUsage{PromptTokens: &prompt, OutputTokens: &output}
		}
		if _, e := engine.ResolveReservation(context.Background(), pre.BillingID(), f.admin.ID, input); e != nil {
			t.Fatal(e)
		}
		before := ownerLedgerFinancialSnapshot(t, f.store)
		detail, e := repository.Read(context.Background(), f.access(), pre.BillingID())
		if e != nil || detail.ResolutionAction != action || detail.Reservation.State != ledger.Settled || detail.Reservation.HeldUnits != "0" || detail.Reservation.ChargeUnits == nil || *detail.Reservation.ChargeUnits != want {
			t.Fatal("existing terminal action/charge changed", detail, e)
		}
		raw, _ := json.Marshal(detail)
		for _, hidden := range []string{"private resolution", "actor", "evidence", "reason", "provider", "calculated_units", "cost_known"} {
			if strings.Contains(string(raw), hidden) {
				t.Fatal("private decision data exposed", string(raw))
			}
		}
		if !bytes.Equal(before, ownerLedgerFinancialSnapshot(t, f.store)) {
			t.Fatal("owner reader changed a terminal decision")
		}
	}
}

func TestOwnerLedgerCorruptMoneyStatesAndDecisionsFailWithoutPartialData(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	row := ownerLedgerReservation(t, f, 40, f.owner.ID, f.key, ledger.Settled, 100, 10)
	repository := f.store.OwnerLedger()
	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		{"negative-reserve", map[string]any{"reserved_units": -1}},
		{"negative-charge", map[string]any{"charge_units": -1}},
		{"over-hold-charge", map[string]any{"charge_units": 101}},
		{"unknown-state", map[string]any{"state": "future-financial-state"}},
		{"unsettled-charge", map[string]any{"state": ledger.Pending}},
		{"noninteger-charge", map[string]any{"charge_units": "private damaged integer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if e := f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", row.ID).Updates(map[string]any{"reserved_units": 100, "charge_units": 10, "state": ledger.Settled}).Error; e != nil {
				t.Fatal(e)
			}
			if e := f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", row.ID).Updates(tc.patch).Error; e != nil {
				t.Fatal(e)
			}
			before := ownerLedgerFinancialSnapshot(t, f.store)
			if detail, e := repository.Read(context.Background(), f.access(), row.ID); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(detail, ownerledger.Detail{}) {
				t.Fatal("bad detail partially escaped", detail, e)
			}
			if page, e := repository.List(context.Background(), f.access(), ownerledger.Query{}); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(page, ownerledger.Page{}) {
				t.Fatal("bad list partially escaped", page, e)
			}
			if !bytes.Equal(before, ownerLedgerFinancialSnapshot(t, f.store)) {
				t.Fatal("read repaired or rewrote corrupt financial data")
			}
		})
	}
	if e := f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", row.ID).Updates(map[string]any{"reserved_units": 100, "charge_units": 0, "state": ledger.Settled}).Error; e != nil {
		t.Fatal(e)
	}
	decision := model.BillingResolution{ReservationID: row.ID, Action: "new-action", Reason: "private", Evidence: "private", Fingerprint: strings.Repeat("b", 64)}
	if e := f.store.DB().Create(&decision).Error; e != nil {
		t.Fatal(e)
	}
	if out, e := repository.Read(context.Background(), f.access(), row.ID); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Detail{}) {
		t.Fatal("unknown decision became public action", out, e)
	}
	if e := f.store.DB().Model(&model.BillingResolution{}).Where("reservation_id = ?", row.ID).Update("action", ledger.ResolveWaive).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.store.DB().Model(&model.BillingReservation{}).Where("id = ?", row.ID).Update("state", ledger.Pending).Error; e != nil {
		t.Fatal(e)
	}
	if out, e := repository.Read(context.Background(), f.access(), row.ID); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Detail{}) {
		t.Fatal("decision exposed without terminal state", out, e)
	}
	balance := model.BillingBalance{OwnerType: "user", OwnerID: f.owner.ID}
	if e := f.store.DB().Create(&balance).Error; e != nil {
		t.Fatal(e)
	}
	for _, patch := range []map[string]any{{"used_units": -1, "reserved_units": 0}, {"used_units": 0, "reserved_units": -1}, {"used_units": math.MaxInt64, "reserved_units": 1}} {
		if e := f.store.DB().Model(&model.BillingBalance{}).Where("owner_type = ? AND owner_id = ?", "user", f.owner.ID).Updates(patch).Error; e != nil {
			t.Fatal(e)
		}
		if out, e := repository.Summary(context.Background(), f.access()); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Summary{}) {
			t.Fatal("invalid balance became public amount", out, e)
		}
	}
}

func TestOwnerLedgerBoundsContextsAndSanitizesStorageFailures(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	row := ownerLedgerReservation(t, f, 50, f.owner.ID, f.key, ledger.Settled, 100, 0)
	if e := f.store.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: f.owner.ID, UsedUnits: 19}).Error; e != nil {
		t.Fatal(e)
	}
	repository := f.store.OwnerLedger()
	if out, e := repository.Summary(nil, f.access()); e != ownerledger.ErrInvalid || !reflect.DeepEqual(out, ownerledger.Summary{}) {
		t.Fatal(out, e)
	}
	if out, e := repository.List(nil, f.access(), ownerledger.Query{}); e != ownerledger.ErrInvalid || !reflect.DeepEqual(out, ownerledger.Page{}) {
		t.Fatal(out, e)
	}
	if out, e := repository.Read(nil, f.access(), row.ID); e != ownerledger.ErrInvalid || !reflect.DeepEqual(out, ownerledger.Detail{}) {
		t.Fatal(out, e)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if out, e := repository.Summary(canceled, f.access()); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Summary{}) {
		t.Fatal(out, e)
	}
	var injected bool
	hook := "test-owner-ledger-query-failure"
	if e := f.store.DB().Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("query deadline exceeded five seconds")
		}
		if tx.Statement.Table == "billing_reservations" || tx.Statement.Table == "decision" {
			injected = true
			tx.AddError(errors.New("private SQL/database path failure"))
		}
	}); e != nil {
		t.Fatal(e)
	}
	long, cancelLong := context.WithTimeout(context.Background(), time.Hour)
	defer cancelLong()
	if out, e := repository.Summary(long, f.access()); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Summary{}) {
		t.Fatal("partial summary/storage error escaped", out, e)
	}
	if out, e := repository.Read(long, f.access(), row.ID); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Detail{}) {
		t.Fatal(out, e)
	}
	if !injected {
		t.Fatal("fault was not exercised")
	}
	if e := f.store.DB().Callback().Query().Remove(hook); e != nil {
		t.Fatal(e)
	}
	blocked := f.store.DB().Begin()
	if blocked.Error != nil {
		t.Fatal(blocked.Error)
	}
	defer blocked.Rollback()
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	started := time.Now()
	if out, e := repository.Summary(short, f.access()); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Summary{}) || time.Since(started) > 2*time.Second {
		t.Fatal("blocked read ignored caller deadline", out, e, time.Since(started))
	}
	if e := blocked.Rollback().Error; e != nil {
		t.Fatal(e)
	}
	if e := f.store.Close(); e != nil {
		t.Fatal(e)
	}
	if out, e := repository.Summary(context.Background(), f.access()); e != ownerledger.ErrUnavailable || !reflect.DeepEqual(out, ownerledger.Summary{}) {
		t.Fatal("closed store error escaped", out, e)
	}
}

func TestOwnerLedgerSummaryUsesOneSnapshotDuringRealSettlement(t *testing.T) {
	f := newOwnerLedgerFixture(t)
	if e := f.store.DB().Model(&model.User{}).Where("id = ?", f.owner.ID).Update("used_quota", 0.000000019).Error; e != nil {
		t.Fatal(e)
	}
	engine := ownerLedgerEngine(t, f)
	pre := ownerLedgerPending(t, f, engine)
	writer, e := store.Open(config.DatabaseConfig{Path: f.path})
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Close()
	otherEngine := billing.New(writer.DB(), billing.NewPriceTableWithHistory(writer.PriceData, time.Hour, writer.RecordPriceSnapshot))
	if e = otherEngine.EnableStrict("0.1", time.Minute); e != nil {
		t.Fatal(e)
	}
	readBalance := make(chan struct{})
	settled := make(chan struct{})
	var once sync.Once
	hook := "test-owner-ledger-snapshot"
	if e = f.store.DB().Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "billing_balances" {
			return
		}
		once.Do(func() {
			close(readBalance)
			select {
			case <-settled:
			case <-tx.Statement.Context.Done():
				tx.AddError(errors.New("snapshot coordination deadline"))
			}
		})
	}); e != nil {
		t.Fatal(e)
	}
	defer f.store.DB().Callback().Query().Remove(hook)
	writeResult := make(chan error, 1)
	go func() {
		defer close(settled)
		select {
		case <-readBalance:
		case <-time.After(4 * time.Second):
			writeResult <- errors.New("reader never reached balance snapshot")
			return
		}
		_, err := otherEngine.ResolveReservation(context.Background(), pre.BillingID(), f.admin.ID, billing.ResolutionInput{Action: ledger.ResolveWaive, Reason: "verified unknown execution", Evidence: "actual owner snapshot test"})
		writeResult <- err
	}()
	summary, e := f.store.OwnerLedger().Summary(context.Background(), f.access())
	if e != nil {
		t.Fatal(e)
	}
	if e = <-writeResult; e != nil {
		t.Fatal("real settlement did not commit while reader held snapshot", e)
	}
	if !summary.BalanceKnown || summary.PostedUsedUnits == nil || *summary.PostedUsedUnits != "19" || summary.HeldUnits == nil || *summary.HeldUnits != "100000000" || summary.PendingCount != "1" {
		t.Fatal("summary mixed pre/post-settlement facts", summary)
	}
	after, e := f.store.OwnerLedger().Summary(context.Background(), f.access())
	if e != nil || after.PostedUsedUnits == nil || *after.PostedUsedUnits != "19" || after.HeldUnits == nil || *after.HeldUnits != "0" || after.PendingCount != "0" {
		t.Fatal("next snapshot missed committed settlement", after, e)
	}
}
