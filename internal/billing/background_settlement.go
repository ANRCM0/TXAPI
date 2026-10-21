package billing

import (
	"context"
	"encoding/json"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

func (e *Engine) ReserveBackground(ctx context.Context, tokenID, userID uint, name string, body []byte) (string, time.Time, error) {
	if _, err := ParseResponsesStrictRequest(body); err != nil {
		return "", time.Time{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return "", time.Time{}, ErrProviderLimit
	}
	if _, exists := fields["type"]; exists {
		return "", time.Time{}, ErrProviderLimit
	}
	if _, exists := fields["stream_id"]; exists {
		return "", time.Time{}, ErrProviderLimit
	}
	pre, err := e.Reserve(ctx, tokenID, userID, name)
	if err != nil {
		return "", time.Time{}, err
	}
	deadline, err := e.ReservationDeadline(ctx, pre)
	if err != nil {
		return "", time.Time{}, err
	}
	return pre.BillingID(), deadline, nil
}

// ResumeReservation adopts an existing dual hold. Dispatch requires the exact
// admission policy and an unexpired, never-started reservation; settlement uses
// historical selling prices even after configuration or credentials change.
func (e *Engine) ResumeReservation(ctx context.Context, id string, tokenID, userID uint, name string, dispatch bool) (*PreDeduct, error) {
	if !e.StrictReady() || !intentHex(id, 16) || tokenID == 0 || userID == 0 || name == "" {
		return nil, ErrStrictUnavailable
	}
	var row model.BillingReservation
	if err := e.strict.db.WithContext(ctx).Where("id = ? AND token_id = ? AND user_id = ? AND origin_model = ?", id, tokenID, userID, name).First(&row).Error; err != nil {
		return nil, err
	}
	policy := e.providerCosts.Load()
	if dispatch {
		var attempts int64
		if err := e.strict.db.WithContext(ctx).Model(&model.BillingAttempt{}).Where("reservation_id = ?", id).Count(&attempts).Error; err != nil {
			return nil, err
		}
		version := row.ProviderPolicyVersion
		if row.State != Reserved || row.LeaseUntil <= time.Now().UnixMilli() || attempts != 0 || row.ProviderHeldUnits != 0 || policy == nil || version == "" || policy.version != version || row.ProviderBudgetUnits != policy.budget {
			return nil, ErrProviderContract
		}
	}
	var history model.BillingPriceVersion
	if err := e.strict.db.WithContext(ctx).First(&history, "version = ?", row.PriceVersion).Error; err != nil {
		return nil, err
	}
	prices, err := restoreSnapshot(history.Version, history.Payload)
	if err != nil {
		return nil, err
	}
	if _, ok := prices.price(name); !ok {
		return nil, ErrPriceMissing
	}
	return &PreDeduct{prices: prices, reservation: &Reservation{id: id, origin: name, prices: prices, providerCosts: policy}}, nil
}

func (e *Engine) ReservationDeadline(ctx context.Context, pre *PreDeduct) (time.Time, error) {
	if !e.StrictReady() || pre == nil || pre.BillingID() == "" {
		return time.Time{}, ErrStrictUnavailable
	}
	var row model.BillingReservation
	if err := e.strict.db.WithContext(ctx).First(&row, "id = ?", pre.BillingID()).Error; err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(row.LeaseUntil), nil
}

// CompleteBackgroundReservation records only a known terminal receipt. Unknown
// remote execution leaves the attempt open so a later durable native receipt can
// still settle it once. Polling and cancellation never create another attempt.
func (e *Engine) CompleteBackgroundReservation(ctx context.Context, id string, tokenID, userID uint, name string, u *execution.Usage, complete bool) error {
	pre, err := e.ResumeReservation(ctx, id, tokenID, userID, name, false)
	if err != nil {
		return err
	}
	var attempt model.BillingAttempt
	found := e.strict.db.WithContext(ctx).Where("reservation_id = ?", id).Order("number DESC").Limit(1).Find(&attempt)
	if found.Error != nil {
		return found.Error
	}
	if found.RowsAffected == 0 {
		var accepted int64
		if err := e.strict.db.WithContext(ctx).Model(&model.BackgroundJob{}).Where("billing_id = ? AND stage NOT IN ?", id, []string{"terminal", "uncertain"}).Count(&accepted).Error; err != nil {
			return err
		}
		// A lost admission acknowledgment must not refund a committed task.
		if accepted != 0 {
			return nil
		}
	}
	if complete && u != nil && found.RowsAffected == 1 {
		if err := e.EndAttempt(ctx, pre, attempt.Number, 200, "success", u); err != nil {
			return err
		}
	}
	_, _, err = e.Finish(ctx, pre, u, attempt.ActualModel, attempt.Group, complete)
	return err
}
