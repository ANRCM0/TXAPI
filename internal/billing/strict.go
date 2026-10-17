package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"regexp"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
)

var (
	ErrStrictUnavailable  = errors.New("strict billing is unavailable")
	ErrPriceMissing       = errors.New("strict billing requires an applied model price")
	ErrSettlementConflict = errors.New("conflicting settlement for the same reservation")
)

const (
	Reserved             = "reserved"
	Active               = "active"
	Pending              = "pending"
	Settled              = "settled"
	StrictEnabledSetting = "billing.strict_enabled"
)

// StrictLedger protects collected user charges, not a provider's behavior.
// Every request holds the entire configured fee ceiling, covering input and
// output together. An actual price above it is held for reconciliation, never
// supplemented beyond the reservation or silently written off as free.
type StrictLedger struct {
	db      *gorm.DB
	prices  *PriceTable
	ceiling int64
	lease   time.Duration
}

type Reservation struct {
	id            string
	prices        *PriceSnapshot
	origin        string
	providerCosts *ProviderCosts
}

func (e *Engine) EnableStrict(ceiling string, lease time.Duration) error {
	if e.db == nil || e.prices == nil || e.prices.recorder == nil || lease <= 0 {
		return ErrStrictUnavailable
	}
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]{1,9})?$`).MatchString(ceiling) {
		return errors.New("billing.max_request_cost requires a positive decimal with at most nine places")
	}
	r, ok := new(big.Rat).SetString(ceiling)
	if !ok || r.Sign() <= 0 {
		return errors.New("billing.max_request_cost must be a positive decimal")
	}
	r.Mul(r, new(big.Rat).SetInt64(UnitScale))
	if !r.IsInt() || !r.Num().IsInt64() {
		return errors.New("billing.max_request_cost requires at most nine decimal places and int64 units")
	}
	for _, table := range []any{&model.BillingBalance{}, &model.BillingReservation{}, &model.BillingAttempt{}, &model.BillingPriceVersion{}, &model.BillingResolution{}} {
		if !e.db.Migrator().HasTable(table) {
			return ErrStrictUnavailable
		}
	}
	if err := e.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Setting{Key: StrictEnabledSetting, Value: "true"}).Error; err != nil {
		return err
	}
	e.strict = &StrictLedger{db: e.db, prices: e.prices, ceiling: r.Num().Int64(), lease: lease}
	return nil
}

func (e *Engine) StrictReady() bool { return e != nil && e.strict != nil }

func (e *Engine) Reserve(ctx context.Context, tokenID, userID uint, origin string) (*PreDeduct, error) {
	row, pre, err := e.prepareReservation(tokenID, userID, origin)
	if err != nil {
		return nil, err
	}
	if err := e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return reserveReservation(tx, row)
	}); err != nil {
		return nil, err
	}
	return pre, nil
}

func (e *Engine) prepareReservation(tokenID, userID uint, origin string) (model.BillingReservation, *PreDeduct, error) {
	if !e.StrictReady() || tokenID == 0 || userID == 0 {
		return model.BillingReservation{}, nil, ErrStrictUnavailable
	}
	prices := e.strict.prices.Snapshot()
	if _, found := prices.price(origin); !found {
		return model.BillingReservation{}, nil, ErrPriceMissing
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return model.BillingReservation{}, nil, err
	}
	row := model.BillingReservation{ID: hex.EncodeToString(raw[:]), UserID: userID, TokenID: tokenID, OriginModel: origin,
		PriceVersion: prices.Version(), ReservedUnits: e.strict.ceiling, State: Reserved, LeaseUntil: time.Now().Add(e.strict.lease).UnixMilli()}
	providerCosts := e.providerCosts.Load()
	if providerCosts != nil {
		row.ProviderBudgetUnits = providerCosts.budget
	}
	pre := &PreDeduct{prices: prices, reservation: &Reservation{id: row.ID, prices: prices, origin: origin, providerCosts: providerCosts}}
	return row, pre, nil
}

// Both ordinary requests and durable intent admission use this exact dual hold.
// The caller owns the transaction; no connection or callback escapes it.
func reserveReservation(tx *gorm.DB, row model.BillingReservation) error {
	var token model.Token
	if err := tx.Where("id = ? AND user_id = ?", row.TokenID, row.UserID).First(&token).Error; err != nil {
		return err
	}
	var user model.User
	if err := tx.First(&user, row.UserID).Error; err != nil {
		return err
	}
	for _, target := range []struct {
		kind        string
		id          uint
		quota, used float64
	}{
		{"user", row.UserID, user.Quota, user.UsedQuota}, {"key", row.TokenID, token.Quota, token.UsedQuota},
	} {
		if err := reserveBalance(tx, target.kind, target.id, target.quota, target.used, row.ReservedUnits); err != nil {
			return err
		}
	}
	return tx.Create(&row).Error
}

func reserveBalance(tx *gorm.DB, kind string, id uint, quota, used float64, amount int64) error {
	limit, err := decimalUnitsRounded(quota, false)
	if err != nil {
		return err
	}
	var existing model.BillingBalance
	result := tx.Where("owner_type=? AND owner_id=?", kind, id).Limit(1).Find(&existing)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		opening, err := decimalUnits(used)
		if err != nil {
			return err
		}
		balance := model.BillingBalance{OwnerType: kind, OwnerID: id, UsedUnits: opening}
		if err := tx.Create(&balance).Error; err != nil {
			return err
		}
	}
	bound := int64(math.MaxInt64)
	if quota > 0 {
		bound = limit
	}
	if amount > bound {
		return ErrQuotaExceeded
	}
	result = tx.Model(&model.BillingBalance{}).
		Where("owner_type = ? AND owner_id = ? AND used_units <= ? AND reserved_units <= ? - used_units", kind, id, bound-amount, bound-amount).
		Update("reserved_units", gorm.Expr("reserved_units + ?", amount))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQuotaExceeded
	}
	return projectBalance(tx, kind, id)
}

func projectBalance(tx *gorm.DB, kind string, id uint) error {
	var b model.BillingBalance
	if err := tx.First(&b, "owner_type=? AND owner_id=?", kind, id).Error; err != nil {
		return err
	}
	table := usersTable
	if kind == "key" {
		table = tokensTable
	}
	// Compatibility projection includes outstanding holds. The authoritative
	// balance and all strict arithmetic remain integer-valued.
	return tx.Table(table).Where("id=?", id).
		Update("used_quota", float64(b.UsedUnits+b.ReservedUnits)/float64(UnitScale)).Error
}

func (e *Engine) StartAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt) error {
	// A configured production policy cannot be bypassed via the ledger-only API.
	if (e != nil && e.providerCosts.Load() != nil) ||
		(pre != nil && pre.reservation != nil && pre.reservation.providerCosts != nil) {
		return ErrProviderContract
	}
	return e.startAttempt(ctx, pre, attempt, nil)
}

func (e *Engine) startAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, bound *providerBound) error {
	if !e.StrictReady() || pre == nil || pre.reservation == nil {
		return ErrStrictUnavailable
	}
	return e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intents int64
		if err := tx.Model(&model.GenerationIntent{}).Where("billing_id = ?", pre.BillingID()).Count(&intents).Error; err != nil {
			return err
		}
		if intents != 0 {
			return ErrStrictUnavailable // Intent admission does not grant dispatch.
		}
		query := tx.Model(&model.BillingReservation{}).
			Where("id=? AND state IN ? AND lease_until>?", pre.BillingID(), []string{Reserved, Active}, time.Now().UnixMilli())
		updates := map[string]any{"state": Active}
		if bound != nil {
			// Never release prior attempt envelopes, even on a failed/unknown
			// receipt. Admission and durable attempt creation commit together.
			query = query.Where("reserved_units >= ? AND provider_budget_units >= ? AND provider_held_units <= provider_budget_units - ?", bound.feeUnits, bound.units, bound.units)
			updates["provider_held_units"] = gorm.Expr("provider_held_units + ?", bound.units)
			attempt.ProviderBoundUnits = bound.units
			attempt.ProviderContractVersion = bound.version
			attempt.ProviderContractSnapshot = bound.snapshot
		}
		res := query.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			if bound != nil {
				var row model.BillingReservation
				if err := tx.First(&row, "id=?", pre.BillingID()).Error; err != nil {
					return err
				}
				if (row.State != Reserved && row.State != Active) || row.LeaseUntil <= time.Now().UnixMilli() {
					return ErrStrictUnavailable
				}
				if row.ReservedUnits < bound.feeUnits {
					return ErrFeeBound
				}
				return ErrProviderBudget
			}
			return ErrStrictUnavailable
		}
		attempt.ReservationID, attempt.State = pre.BillingID(), Active
		return tx.Create(&attempt).Error
	})
}

func snapshotPriceModel(s *PriceSnapshot, actual, origin string) string {
	if _, found := s.price(actual); !found {
		return origin
	}
	return actual
}

func snapshotCost(s *PriceSnapshot, u *relaymodel.Usage, actual, origin, group string) (int64, error) {
	return s.CostUnits(u, snapshotPriceModel(s, actual, origin), group)
}

func (e *Engine) EndAttempt(ctx context.Context, pre *PreDeduct, number, status int, termination string, u *relaymodel.Usage) error {
	if !e.StrictReady() || pre == nil || pre.reservation == nil {
		return ErrStrictUnavailable
	}
	var attempt model.BillingAttempt
	db := e.strict.db.WithContext(ctx)
	if err := db.First(&attempt, "reservation_id=? AND number=?", pre.BillingID(), number).Error; err != nil {
		return err
	}
	attempt.State, attempt.Status, attempt.Termination = "finished", status, termination
	var contractErr error
	if u != nil && attempt.ProviderContractVersion != "" {
		var snapshot providerAttemptSnapshot
		hash := sha256.Sum256([]byte(attempt.ProviderContractSnapshot))
		if json.Unmarshal([]byte(attempt.ProviderContractSnapshot), &snapshot) != nil ||
			hex.EncodeToString(hash[:]) != attempt.ProviderContractVersion || (snapshot.Format != 1 && snapshot.Format != 2) || snapshot.UnitScale != UnitScale ||
			int64(u.PromptTokens) > snapshot.Contract.InputTokenCeiling || int64(u.CompletionTokens) > snapshot.OutputLimit {
			contractErr = ErrProviderLimit
			attempt.Termination = "provider_contract_violation"
		}
	}
	if u != nil {
		attempt.PromptTokens, attempt.OutputTokens = u.PromptTokens, u.CompletionTokens
		cost, err := snapshotCost(pre.prices, u, attempt.ActualModel, pre.reservation.origin, attempt.Group)
		if err == nil && completeUsage(u) {
			attempt.CostUnits, attempt.CostKnown = cost, termination == "success" && status == 200 && contractErr == nil
		}
	}
	if err := db.Save(&attempt).Error; err != nil {
		return err
	}
	return contractErr
}

// Finish is idempotent, including across lost acknowledgments and restarts.
// Unknown execution/usage keeps the complete hold; absence of a started attempt
// proves no upstream execution and allows a full release.
func (e *Engine) Finish(ctx context.Context, pre *PreDeduct, u *relaymodel.Usage, actual, group string, complete bool) (charge int64, state string, err error) {
	if !e.StrictReady() || pre == nil || pre.reservation == nil {
		return 0, "", ErrStrictUnavailable
	}
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var finishErr error
		charge, state, finishErr = finishReservation(tx, pre, u, actual, group, complete)
		return finishErr
	})
	return
}

func finishReservation(tx *gorm.DB, pre *PreDeduct, u *relaymodel.Usage, actual, group string, complete bool) (charge int64, state string, err error) {
	err = func() error {
		var row model.BillingReservation
		if err := tx.First(&row, "id=?", pre.BillingID()).Error; err != nil {
			return err
		}
		var attempts int64
		if err := tx.Model(&model.BillingAttempt{}).Where("reservation_id=?", row.ID).Count(&attempts).Error; err != nil {
			return err
		}
		actualCost, reason := int64(0), ""
		refusal := persistedProviderRefusal(row.Reason)
		if attempts > 0 {
			if !complete || !completeUsage(u) {
				reason = "execution_or_usage_unknown"
				if refusal != "" {
					reason += ":" + refusal
				}
			} else {
				var costErr error
				actualCost, costErr = snapshotCost(pre.prices, u, actual, row.OriginModel, group)
				if costErr != nil || actualCost > row.ReservedUnits {
					reason = "cost_exceeds_hold_or_invalid_usage"
				}
			}
		}
		if reason != "" {
			if row.State == Settled {
				if complete && u != nil {
					return ErrSettlementConflict
				}
				charge, state = row.ChargeUnits, row.State
				return nil
			}
			state = Pending
			return tx.Model(&row).Updates(map[string]any{"state": Pending, "reason": reason}).Error
		}
		prompt, output := 0, 0
		if u != nil {
			prompt, output = u.PromptTokens, u.CompletionTokens
		}
		fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d/%d/%s/%s", row.ID, actualCost, prompt, output, actual, group)))
		digest := hex.EncodeToString(fingerprint[:])
		if row.State == Settled {
			if row.Fingerprint != digest {
				return ErrSettlementConflict
			}
			charge, state = row.ChargeUnits, Settled
			return nil
		}
		if err := settleBalances(tx, row, actualCost); err != nil {
			return err
		}
		charge, state = actualCost, Settled
		if attempts > 0 {
			refusal = ""
		}
		return tx.Model(&row).Updates(map[string]any{"state": Settled, "reason": refusal, "charge_units": actualCost, "fingerprint": digest}).Error
	}()
	return
}

func settleBalances(tx *gorm.DB, row model.BillingReservation, charge int64) error {
	if charge < 0 || charge > row.ReservedUnits {
		return errors.New("settlement exceeds strict budget hold")
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", row.UserID}, {"key", row.TokenID}} {
		res := tx.Model(&model.BillingBalance{}).
			Where("owner_type=? AND owner_id=? AND reserved_units>=?", target.kind, target.id, row.ReservedUnits).
			Updates(map[string]any{"reserved_units": gorm.Expr("reserved_units - ?", row.ReservedUnits), "used_units": gorm.Expr("used_units + ?", charge)})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("inconsistent strict budget hold")
		}
		if err := projectBalance(tx, target.kind, target.id); err != nil {
			return err
		}
	}
	return nil
}

func completeUsage(u *relaymodel.Usage) bool {
	if u == nil || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.PromptTokens > math.MaxInt-u.CompletionTokens {
		return false
	}
	return u.TotalTokens == 0 || u.TotalTokens == u.PromptTokens+u.CompletionTokens
}

// RecoverExpired never refunds a possibly executed request. It is safe to
// repeat and also repairs reservations abandoned before their first attempt.
func (e *Engine) RecoverExpired(ctx context.Context, now time.Time) error {
	if !e.StrictReady() {
		return ErrStrictUnavailable
	}
	if _, err := e.RecoverGenerationIntents(ctx, now, 200); err != nil {
		return err
	}
	var rows []model.BillingReservation
	if err := e.strict.db.WithContext(ctx).Where("state IN ? AND lease_until <= ?", []string{Reserved, Active}, now.UnixMilli()).
		Where("NOT EXISTS (SELECT 1 FROM generation_intents gi WHERE gi.billing_id = billing_reservations.id)").Limit(200).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var receipt model.BillingAttempt
		result := e.strict.db.WithContext(ctx).Where("reservation_id=?", row.ID).Order("number DESC").Limit(1).Find(&receipt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 && receipt.State == "finished" && receipt.CostKnown {
			u := &relaymodel.Usage{PromptTokens: receipt.PromptTokens, CompletionTokens: receipt.OutputTokens}
			if _, _, err := e.ReconcileKnownUsage(ctx, row.ID, u, receipt.ActualModel, receipt.Group); err != nil {
				return err
			}
			continue
		}
		pre := &PreDeduct{reservation: &Reservation{id: row.ID, origin: row.OriginModel}}
		if _, _, err := e.Finish(ctx, pre, nil, "", "", false); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileKnownUsage is an internal operator seam, not a public API. It uses
// the historical snapshot, not today's prices, and cannot exceed the old hold.
func (e *Engine) ReconcileKnownUsage(ctx context.Context, id string, u *relaymodel.Usage, actual, group string) (int64, string, error) {
	if !e.StrictReady() {
		return 0, "", ErrStrictUnavailable
	}
	var row model.BillingReservation
	if err := e.strict.db.WithContext(ctx).First(&row, "id=?", id).Error; err != nil {
		return 0, "", err
	}
	var history model.BillingPriceVersion
	if err := e.strict.db.WithContext(ctx).First(&history, "version=?", row.PriceVersion).Error; err != nil {
		return 0, "", err
	}
	prices, err := restoreSnapshot(history.Version, history.Payload)
	if err != nil {
		return 0, "", err
	}
	pre := &PreDeduct{prices: prices, reservation: &Reservation{id: id, origin: row.OriginModel, prices: prices}}
	return e.Finish(ctx, pre, u, actual, group, true)
}

func (e *Engine) StartRecovery(interval time.Duration) func() {
	if !e.StrictReady() {
		return func() {}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := e.RecoverExpired(ctx, time.Now())
			cancel()
			if err != nil {
				slog.Error("strict reservation recovery failed; holds retained", "err", err)
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop); <-done }) }
}
