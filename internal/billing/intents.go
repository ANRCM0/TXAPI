package billing

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
)

func intentHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && hex.EncodeToString(decoded) == value
}

// AdmitGenerationIntent accepts opaque, encrypted input and a trusted owner/Key
// scope. The application refreshes authority first. Admission is not a claim or
// permission to execute; the dispatcher must adopt the original reservation.
func (e *Engine) AdmitGenerationIntent(ctx context.Context, in model.GenerationIntent, limits model.GenerationIntentLimits) (out model.GenerationIntent, err error) {
	if !e.StrictReady() {
		return out, ErrStrictUnavailable
	}
	now := time.Now().UnixMilli()
	if !intentHex(in.ID, 32) || !intentHex(in.AdmissionKey, 32) || !intentHex(in.RequestDigest, 32) || !intentHex(in.DigestVersion, 32) || in.UserID == 0 || in.TokenID == 0 ||
		(in.Operation != "chat" && in.Operation != "embeddings") || in.Model == "" || len(in.Model) > 256 || in.State != model.IntentQueued || in.BillingID != in.ID[:32] ||
		len(in.Ciphertext) < 30 || len(in.Ciphertext) > 68<<10 || in.CreatedAt <= 0 || in.CreatedAt > now || in.Deadline <= now ||
		in.Deadline-in.CreatedAt > int64((5*time.Minute)/time.Millisecond) || in.ExpiresAt < in.Deadline || in.ExpiresAt-in.CreatedAt > int64((7*24*time.Hour)/time.Millisecond) ||
		limits.InstanceRecords < 2 || limits.InstanceRecords > 4096 || limits.OwnerRecords < 1 || limits.OwnerRecords >= limits.InstanceRecords ||
		limits.InstanceBytes < limits.OwnerBytes || limits.InstanceBytes > 64<<20 || limits.OwnerBytes < int64(len(in.Ciphertext)) ||
		in.ClaimEpoch != 0 || in.ClaimHash != "" || in.ClaimUntil != 0 || in.CancelRequested || in.ProviderPolicyVersion != "" || in.OutputRecordID != "" {
		return out, model.ErrIntentCapacity
	}
	// Price history may use this same SQLite connection, so load it before the
	// transaction. A retry still reads its old receipt even after prices change.
	row, prepared, preparationErr := e.prepareReservation(in.TokenID, in.UserID, in.Model)
	if preparationErr == nil && prepared.reservation.providerCosts != nil {
		in.ProviderPolicyVersion = prepared.reservation.providerCosts.version
	}
	in.Ciphertext = append([]byte(nil), in.Ciphertext...)
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Changing the HMAC secret must fail closed, not silently create another
		// idempotency namespace and another hold for the same incoming key.
		const digestSetting = "generation.intent_digest_version"
		var configured model.Setting
		version := tx.Where("key = ?", digestSetting).Limit(1).Find(&configured)
		if version.Error != nil {
			return version.Error
		}
		if version.RowsAffected == 0 {
			if err := tx.Create(&model.Setting{Key: digestSetting, Value: in.DigestVersion}).Error; err != nil {
				return err
			}
		} else if configured.Value != in.DigestVersion {
			return model.ErrIntentConflict
		}
		found := tx.Omit("ciphertext").Where("user_id = ? AND token_id = ? AND admission_key = ?", in.UserID, in.TokenID, in.AdmissionKey).Limit(1).Find(&out)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 1 {
			if out.ExpiresAt <= time.Now().UnixMilli() {
				return model.ErrIntentNotFound
			}
			if out.RequestDigest != in.RequestDigest || out.Operation != in.Operation || out.Model != in.Model {
				return model.ErrIntentConflict
			}
			var readErr error
			out, readErr = readIntent(tx, in.UserID, in.TokenID, out.ID)
			return readErr
		}
		if preparationErr != nil {
			return preparationErr
		}
		row.ID = in.BillingID
		if in.Deadline > row.LeaseUntil {
			return model.ErrIntentCapacity
		}
		for _, owner := range []bool{false, true} {
			q := tx.Model(&model.GenerationIntent{})
			maxBytes, maxCount := limits.InstanceBytes, limits.InstanceRecords
			if owner {
				q = q.Where("user_id = ?", in.UserID)
				maxBytes, maxCount = limits.OwnerBytes, limits.OwnerRecords
			}
			var usage struct{ Bytes, Count int64 }
			if err := q.Select("COALESCE(SUM(length(ciphertext)),0) AS bytes, COUNT(*) AS count").Scan(&usage).Error; err != nil {
				return err
			}
			if usage.Bytes > maxBytes-int64(len(in.Ciphertext)) || usage.Count >= maxCount {
				return model.ErrIntentCapacity
			}
		}
		if err := reserveReservation(tx, row); err != nil {
			return err
		}
		if err := tx.Create(&in).Error; err != nil {
			return err
		}
		out = in
		return nil
	})
	if err != nil {
		out = model.GenerationIntent{}
	}
	return
}

func readIntent(tx *gorm.DB, userID, tokenID uint, id string) (out model.GenerationIntent, err error) {
	if !intentHex(id, 32) || userID == 0 || tokenID == 0 {
		return out, model.ErrIntentNotFound
	}
	q := tx.Where("id = ? AND user_id = ? AND token_id = ? AND expires_at > ?", id, userID, tokenID, time.Now().UnixMilli())
	// Reject damaged lengths before materializing a blob, not after allocating it.
	q = q.Where("length(ciphertext) BETWEEN 30 AND ?", 68<<10)
	if err := q.First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = model.ErrIntentNotFound
		}
		return model.GenerationIntent{}, err
	}
	return out, nil
}

func (e *Engine) ReadGenerationIntent(ctx context.Context, userID, tokenID uint, id string) (model.GenerationIntent, error) {
	if !e.StrictReady() {
		return model.GenerationIntent{}, ErrStrictUnavailable
	}
	return readIntent(e.strict.db.WithContext(ctx), userID, tokenID, id)
}

// CancelGenerationIntent can release only a proved unstarted reservation. A
// changed/attempted intent becomes uncertain, never free or eligible for replay.
func (e *Engine) CancelGenerationIntent(ctx context.Context, userID, tokenID uint, id string) (out model.GenerationIntent, err error) {
	if !e.StrictReady() {
		return out, ErrStrictUnavailable
	}
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var readErr error
		out, readErr = readIntent(tx, userID, tokenID, id)
		if readErr != nil {
			return readErr
		}
		if out.State != model.IntentQueued && out.State != model.IntentClaimed && out.State != model.IntentRunning {
			return nil
		}
		if out.State == model.IntentRunning {
			var attempts int64
			if err := tx.Model(&model.BillingAttempt{}).Where("reservation_id = ?", out.BillingID).Count(&attempts).Error; err != nil {
				return err
			}
			if attempts > 0 {
				out.CancelRequested = true
				return tx.Model(&model.GenerationIntent{}).Where("id = ?", out.ID).Update("cancel_requested", true).Error
			}
		}
		return releaseIntent(tx, &out, model.IntentCancelled)
	})
	if err != nil {
		out = model.GenerationIntent{}
	}
	return
}

func releaseIntent(tx *gorm.DB, in *model.GenerationIntent, state string) error {
	var row model.BillingReservation
	if err := tx.First(&row, "id = ? AND user_id = ? AND token_id = ?", in.BillingID, in.UserID, in.TokenID).Error; err != nil {
		return err
	}
	var attempts int64
	if err := tx.Model(&model.BillingAttempt{}).Where("reservation_id = ?", row.ID).Count(&attempts).Error; err != nil {
		return err
	}
	if attempts != 0 || row.State != Reserved || row.ProviderHeldUnits != 0 || row.OriginModel != in.Model {
		in.State = model.IntentUncertain
		if row.State != Settled {
			var receipt model.BillingAttempt
			latest := tx.Where("reservation_id = ?", row.ID).Order("number DESC").Limit(1).Find(&receipt)
			if latest.Error != nil {
				return latest.Error
			}
			if latest.RowsAffected == 1 && receipt.State == "finished" && receipt.CostKnown {
				var history model.BillingPriceVersion
				if err := tx.First(&history, "version = ?", row.PriceVersion).Error; err != nil {
					return err
				}
				prices, err := restoreSnapshot(history.Version, history.Payload)
				if err != nil {
					return err
				}
				pre := &PreDeduct{prices: prices, reservation: &Reservation{id: row.ID, origin: row.OriginModel, prices: prices}}
				usage := receiptUsage(receipt)
				if _, _, err := finishReservation(tx, pre, usage, receipt.ActualModel, receipt.Group, true); err != nil {
					return err
				}
			} else {
				if err := tx.Model(&row).Updates(map[string]any{"state": Pending, "reason": "intent_execution_unknown"}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(in).Updates(map[string]any{"state": in.State, "claim_hash": "", "claim_until": 0}).Error
	}
	pre := &PreDeduct{reservation: &Reservation{id: row.ID, origin: row.OriginModel}}
	if charge, settled, err := finishReservation(tx, pre, nil, "", "", false); err != nil {
		return err
	} else if charge != 0 || settled != Settled {
		return model.ErrIntentUnsafe
	}
	previous := in.State
	in.State = state
	changed := tx.Model(&model.GenerationIntent{}).Where("id = ? AND state = ?", in.ID, previous).
		Updates(map[string]any{"state": state, "claim_hash": "", "claim_until": 0})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return model.ErrIntentUnsafe
	}
	return nil
}

func (e *Engine) RecoverGenerationIntents(ctx context.Context, now time.Time, limit int) (int64, error) {
	if !e.StrictReady() {
		return 0, ErrStrictUnavailable
	}
	if limit < 1 || limit > 200 {
		return 0, model.ErrIntentCapacity
	}
	var recovered int64
	err := e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.GenerationIntent
		// Recovery does not read or decrypt request bodies and never returns work.
		// Exclude reclaimable, unstarted epochs before LIMIT. Otherwise an old
		// batch of them can starve recovery of genuinely attempted expired work.
		if err := tx.Omit("ciphertext").Where("(state = ? AND deadline <= ?) OR (state IN ? AND (deadline <= ? OR (claim_until <= ? AND EXISTS (SELECT 1 FROM billing_attempts ba WHERE ba.reservation_id = generation_intents.billing_id)))) OR (state = ? AND claim_until <= ?)",
			model.IntentQueued, now.UnixMilli(), []string{model.IntentClaimed, model.IntentRunning}, now.UnixMilli(), now.UnixMilli(), model.IntentFinalizing, now.UnixMilli()).Order("deadline, id").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if rows[i].State == model.IntentFinalizing {
				var ready int64
				if err := tx.Model(&model.GenerationRecord{}).Where("id = ? AND user_id = ? AND token_id = ? AND state = ? AND committed = ?", rows[i].OutputRecordID, rows[i].UserID, rows[i].TokenID, "sealed", true).
					Where("EXISTS (SELECT 1 FROM billing_reservations br WHERE br.id = ? AND br.state = ? AND br.user_id = ? AND br.token_id = ? AND br.origin_model = ?)",
						rows[i].BillingID, Settled, rows[i].UserID, rows[i].TokenID, rows[i].Model).Count(&ready).Error; err != nil {
					return err
				}
				if ready == 1 {
					if err := tx.Model(&model.GenerationIntent{}).Where("id = ?", rows[i].ID).Updates(map[string]any{"state": model.IntentSucceeded, "claim_hash": "", "claim_until": 0}).Error; err != nil {
						return err
					}
					recovered++
					continue
				}
			}
			if rows[i].Deadline > now.UnixMilli() {
				var attempts int64
				if err := tx.Model(&model.BillingAttempt{}).Where("reservation_id = ?", rows[i].BillingID).Count(&attempts).Error; err != nil {
					return err
				}
				// Expired but proved-unstarted work remains claimable until its
				// original deadline. New epochs fence the paused old executor.
				if attempts == 0 {
					continue
				}
			}
			if err := releaseIntent(tx, &rows[i], model.IntentExpired); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return recovered, nil
}

// CleanupGenerationIntents erases expired content independently of financial
// resolution. Pending execution retains only metadata/idempotency and its holds;
// settled terminal metadata can be removed without deleting the financial audit.
func (e *Engine) CleanupGenerationIntents(ctx context.Context, limit int) (int64, error) {
	if !e.StrictReady() {
		return 0, ErrStrictUnavailable
	}
	if limit < 1 || limit > 200 {
		return 0, model.ErrIntentCapacity
	}
	var cleaned int64
	err := e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.GenerationIntent
		if err := tx.Omit("ciphertext").Where("expires_at <= ?", time.Now().UnixMilli()).
			Where("length(ciphertext) > 0 OR EXISTS (SELECT 1 FROM billing_reservations br WHERE br.id = generation_intents.billing_id AND br.state = ? AND br.user_id = generation_intents.user_id AND br.token_id = generation_intents.token_id)", Settled).
			Order("expires_at, id").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			terminal := row.State == model.IntentCancelled || row.State == model.IntentExpired || row.State == model.IntentSucceeded || row.State == model.IntentFailed || row.State == model.IntentUncertain
			if terminal {
				deleted := tx.Where("id = ?", row.ID).
					Where("EXISTS (SELECT 1 FROM billing_reservations br WHERE br.id = generation_intents.billing_id AND br.state = ? AND br.user_id = generation_intents.user_id AND br.token_id = generation_intents.token_id)", Settled).
					Delete(&model.GenerationIntent{})
				if deleted.Error != nil {
					return deleted.Error
				}
				if deleted.RowsAffected == 1 {
					cleaned++
					continue
				}
			}
			purged := tx.Model(&model.GenerationIntent{}).Where("id = ? AND length(ciphertext) > 0", row.ID).Update("ciphertext", []byte{})
			if purged.Error != nil {
				return purged.Error
			}
			cleaned += purged.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return cleaned, nil
}
