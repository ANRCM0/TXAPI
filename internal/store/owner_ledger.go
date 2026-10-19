package store

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"
	ledger "minigateway/internal/foundation/contracts/ledger/v1"
	ownerledger "minigateway/internal/foundation/contracts/ownerledger/v1"
)

type ownerLedger struct{ store *Store }

func (s *Store) OwnerLedger() ownerledger.Repository { return &ownerLedger{store: s} }

// The projection deliberately never loads administrator-only financial columns.
const ownerLedgerColumns = "id,token_id,origin_model,price_version,reserved_units,charge_units,state,lease_until,created_at,updated_at"

type ownerLedgerRow struct {
	ID, OriginModel, PriceVersion, State string
	TokenID                              uint
	ReservedUnits, ChargeUnits           *int64
	LeaseUntil                           int64
	CreatedAt, UpdatedAt                 time.Time
}

func ownerLedgerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(decoded) == id
}

func ownerLedgerState(state string) bool {
	switch state {
	case ledger.Reserved, ledger.Active, ledger.Pending, ledger.Settled:
		return true
	}
	return false
}

func ownerLedgerError(err error) error {
	if err == nil {
		return nil
	}
	for _, fixed := range []error{ownerledger.ErrInvalid, ownerledger.ErrUnauthorized, ownerledger.ErrNotFound, ownerledger.ErrUnavailable} {
		if errors.Is(err, fixed) {
			return fixed
		}
	}
	return ownerledger.ErrUnavailable
}

func (r *ownerLedger) read(ctx context.Context, access ownerledger.Access, fn func(*gorm.DB, uint) error) error {
	if ctx == nil {
		return ownerledger.ErrInvalid
	}
	if r == nil || r.store == nil || r.store.db == nil || ctx.Err() != nil {
		return ownerledger.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, err := currentSessionScope(tx, access.Session, false)
		if errors.Is(err, errSessionUnauthorized) {
			return ownerledger.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		return fn(tx, user.ID)
	})
	return ownerLedgerError(err)
}

func ownerLedgerProjection(row ownerLedgerRow) (ownerledger.Reservation, error) {
	if !ownerLedgerID(row.ID) || row.TokenID == 0 || !ownerLedgerState(row.State) || row.ReservedUnits == nil || row.ChargeUnits == nil ||
		*row.ReservedUnits < 0 || *row.ChargeUnits < 0 || *row.ChargeUnits > *row.ReservedUnits || row.State != ledger.Settled && *row.ChargeUnits != 0 {
		return ownerledger.Reservation{}, ownerledger.ErrUnavailable
	}
	reserved := strconv.FormatInt(*row.ReservedUnits, 10)
	out := ownerledger.Reservation{ID: row.ID, TokenID: row.TokenID, OriginModel: row.OriginModel, PriceVersion: row.PriceVersion, State: row.State,
		OriginalReservedUnits: reserved, HeldUnits: reserved, LeaseUntil: row.LeaseUntil, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.State == ledger.Settled {
		charge := strconv.FormatInt(*row.ChargeUnits, 10)
		out.HeldUnits, out.ChargeUnits = "0", &charge
	}
	return out, nil
}

func (r *ownerLedger) Summary(ctx context.Context, access ownerledger.Access) (out ownerledger.Summary, err error) {
	err = r.read(ctx, access, func(tx *gorm.DB, owner uint) error {
		var balance struct{ UsedUnits, ReservedUnits *int64 }
		result := tx.Table("billing_balances").Select("used_units,reserved_units").Where("owner_type = ? AND owner_id = ?", "user", owner).Limit(1).Find(&balance)
		if result.Error != nil {
			return result.Error
		}
		out.UnitScale = strconv.FormatInt(ledger.UnitScale, 10)
		if result.RowsAffected == 1 {
			if balance.UsedUnits == nil || balance.ReservedUnits == nil || *balance.UsedUnits < 0 || *balance.ReservedUnits < 0 || *balance.UsedUnits > math.MaxInt64-*balance.ReservedUnits {
				return ownerledger.ErrUnavailable
			}
			used, held := strconv.FormatInt(*balance.UsedUnits, 10), strconv.FormatInt(*balance.ReservedUnits, 10)
			out.BalanceKnown, out.PostedUsedUnits, out.HeldUnits = true, &used, &held
		}
		var pending int64
		if e := tx.Table("billing_reservations").Where("user_id = ? AND state = ?", owner, ledger.Pending).Count(&pending).Error; e != nil {
			return e
		}
		out.PendingCount = strconv.FormatInt(pending, 10)
		out.AsOf = time.Now().UnixMilli()
		return nil
	})
	if err != nil {
		out = ownerledger.Summary{}
	}
	return
}

func (r *ownerLedger) List(ctx context.Context, access ownerledger.Access, query ownerledger.Query) (out ownerledger.Page, err error) {
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 100 || query.Offset < 0 || query.Offset > 10000 || query.State != "" && !ownerLedgerState(query.State) {
		return out, ownerledger.ErrInvalid
	}
	err = r.read(ctx, access, func(tx *gorm.DB, owner uint) error {
		q := tx.Table("billing_reservations").Select(ownerLedgerColumns).Where("user_id = ?", owner)
		if query.State != "" {
			q = q.Where("state = ?", query.State)
		}
		if query.TokenID != 0 {
			q = q.Where("token_id = ?", query.TokenID)
		}
		var rows []ownerLedgerRow
		if e := q.Order("created_at DESC,id DESC").Limit(query.Limit + 1).Offset(query.Offset).Find(&rows).Error; e != nil {
			return e
		}
		out = ownerledger.Page{Data: make([]ownerledger.Reservation, 0, min(len(rows), query.Limit)), HasMore: len(rows) > query.Limit}
		for i, row := range rows {
			projected, e := ownerLedgerProjection(row)
			if e != nil {
				return e
			}
			if i < query.Limit {
				out.Data = append(out.Data, projected)
			}
		}
		return nil
	})
	if err != nil {
		out = ownerledger.Page{}
	}
	return
}

func (r *ownerLedger) Read(ctx context.Context, access ownerledger.Access, id string) (out ownerledger.Detail, err error) {
	err = r.read(ctx, access, func(tx *gorm.DB, owner uint) error {
		if !ownerLedgerID(id) {
			return ownerledger.ErrNotFound
		}
		var row ownerLedgerRow
		result := tx.Table("billing_reservations").Select(ownerLedgerColumns).Where("id = ? AND user_id = ?", id, owner).Limit(1).Find(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ownerledger.ErrNotFound
		}
		var e error
		out.Reservation, e = ownerLedgerProjection(row)
		if e != nil {
			return e
		}
		var decision struct{ Action string }
		result = tx.Table("billing_resolutions AS decision").Select("decision.action").
			Joins("JOIN billing_reservations AS reservation ON reservation.id = decision.reservation_id").
			Where("decision.reservation_id = ? AND reservation.user_id = ?", id, owner).Limit(1).Find(&decision)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			if row.State != ledger.Settled {
				return ownerledger.ErrUnavailable
			}
			switch decision.Action {
			case ledger.ResolveUsage, ledger.ResolveCap, ledger.ResolveWaive:
				out.ResolutionAction = decision.Action
			default:
				return ownerledger.ErrUnavailable
			}
		}
		return nil
	})
	if err != nil {
		out = ownerledger.Detail{}
	}
	return
}

var _ ownerledger.Repository = (*ownerLedger)(nil)
