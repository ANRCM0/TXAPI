package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
)

func claimHash(claim model.GenerationClaim) string {
	if !intentHex(claim.ID, 32) || !intentHex(claim.Token, 32) || claim.Epoch < 1 {
		return ""
	}
	sum := sha256.Sum256([]byte(claim.Token))
	return hex.EncodeToString(sum[:])
}

func validClaimDuration(lease time.Duration) bool {
	return lease >= time.Second && lease <= time.Minute
}

// ClaimGenerationIntent only reclaims epochs proved to have no durable attempt.
// Once StartPreparedAttempt commits, this intent is never dispatchable again.
func (e *Engine) ClaimGenerationIntent(ctx context.Context, lease time.Duration) (out model.GenerationIntent, claim model.GenerationClaim, err error) {
	if !e.StrictReady() {
		return out, claim, ErrStrictUnavailable
	}
	if !validClaimDuration(lease) {
		return out, claim, model.ErrIntentCapacity
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return
	}
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UnixMilli()
		found := tx.Where("deadline > ? AND expires_at > ? AND cancel_requested = ?", now, now, false).
			Where("state = ? OR (state IN ? AND claim_until <= ?)", model.IntentQueued, []string{model.IntentClaimed, model.IntentRunning}, now).
			Where("length(ciphertext) BETWEEN 30 AND ?", 68<<10).
			Where("NOT EXISTS (SELECT 1 FROM billing_attempts ba WHERE ba.reservation_id = generation_intents.billing_id)").
			Where("EXISTS (SELECT 1 FROM billing_reservations br WHERE br.id = generation_intents.billing_id AND br.user_id = generation_intents.user_id AND br.token_id = generation_intents.token_id AND br.origin_model = generation_intents.model AND br.state = ? AND br.provider_held_units = 0 AND br.lease_until > ?)", Reserved, now).
			Order("created_at, id").Limit(1).Find(&out)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected != 1 {
			return model.ErrIntentNotFound
		}
		if !intentHex(out.ID, 32) || out.BillingID != out.ID[:32] || out.UserID == 0 || out.TokenID == 0 || out.Model == "" ||
			(out.Operation != "chat" && out.Operation != "embeddings") || out.ClaimEpoch < 0 {
			return model.ErrIntentUnsafe
		}
		var row model.BillingReservation
		if err := tx.First(&row, "id = ? AND user_id = ? AND token_id = ?", out.BillingID, out.UserID, out.TokenID).Error; err != nil {
			return err
		}
		if row.State != Reserved || row.OriginModel != out.Model || row.ProviderHeldUnits != 0 || row.LeaseUntil <= now || out.ClaimEpoch == math.MaxInt64 {
			return model.ErrIntentUnsafe
		}
		claim = model.GenerationClaim{ID: out.ID, Epoch: out.ClaimEpoch + 1, Token: hex.EncodeToString(token[:])}
		until := min(time.Now().Add(lease).UnixMilli(), out.Deadline, row.LeaseUntil)
		res := tx.Model(&model.GenerationIntent{}).Where("id = ? AND state = ? AND claim_epoch = ?", out.ID, out.State, out.ClaimEpoch).
			Updates(map[string]any{"state": model.IntentClaimed, "claim_epoch": claim.Epoch, "claim_hash": claimHash(claim), "claim_until": until, "output_record_id": ""})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return model.ErrClaimLost
		}
		out.State, out.ClaimEpoch, out.ClaimHash, out.ClaimUntil = model.IntentClaimed, claim.Epoch, claimHash(claim), until
		out.OutputRecordID = ""
		return nil
	})
	if err != nil {
		return model.GenerationIntent{}, model.GenerationClaim{}, err
	}
	return
}

func readClaim(tx *gorm.DB, claim model.GenerationClaim) (in model.GenerationIntent, err error) {
	hash := claimHash(claim)
	if hash == "" {
		return in, model.ErrClaimLost
	}
	found := tx.Omit("ciphertext").Where("id = ? AND claim_epoch = ? AND claim_hash = ?", claim.ID, claim.Epoch, hash).Limit(1).Find(&in)
	if found.Error != nil {
		return in, found.Error
	}
	if found.RowsAffected != 1 {
		return in, model.ErrClaimLost
	}
	return in, nil
}

// Renewal does not resurrect expired ownership or extend the admitted deadline.
// A cancellation is a request to stop, not proof of free upstream execution.
func (e *Engine) RenewGenerationClaim(ctx context.Context, claim model.GenerationClaim, lease time.Duration) (cancelRequested bool, err error) {
	if !e.StrictReady() {
		return false, ErrStrictUnavailable
	}
	if !validClaimDuration(lease) {
		return false, model.ErrIntentCapacity
	}
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if (in.State != model.IntentClaimed && in.State != model.IntentRunning) || in.ClaimUntil <= now || in.Deadline <= now {
			return model.ErrClaimLost
		}
		cancelRequested = in.CancelRequested
		return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("claim_until", min(time.Now().Add(lease).UnixMilli(), in.Deadline)).Error
	})
	return
}

// ResumeGenerationIntent reconstructs the original hold, never Reserve again.
// Historical selling prices are restored; provider-policy drift fails closed.
// Capability/routing and actual wire cost checks still run in the coordinator.
func (e *Engine) ResumeGenerationIntent(ctx context.Context, claim model.GenerationClaim, userID, tokenID uint, operation, name string) (pre *PreDeduct, err error) {
	if !e.StrictReady() {
		return nil, ErrStrictUnavailable
	}
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if in.State != model.IntentClaimed || in.CancelRequested || in.ClaimUntil <= now || in.Deadline <= now || in.OutputRecordID == "" ||
			in.UserID != userID || in.TokenID != tokenID || in.Operation != operation || in.Model != name {
			return model.ErrClaimLost
		}
		var row model.BillingReservation
		if err := tx.First(&row, "id = ? AND user_id = ? AND token_id = ?", in.BillingID, userID, tokenID).Error; err != nil {
			return err
		}
		var attempts int64
		if err := tx.Model(&model.BillingAttempt{}).Where("reservation_id = ?", row.ID).Count(&attempts).Error; err != nil {
			return err
		}
		if row.State != Reserved || row.OriginModel != name || row.LeaseUntil <= now || attempts != 0 || row.ProviderHeldUnits != 0 {
			return model.ErrIntentUnsafe
		}
		policy := e.providerCosts.Load()
		version, budget := "", int64(0)
		if policy != nil {
			version, budget = policy.version, policy.budget
		}
		if version != in.ProviderPolicyVersion || budget != row.ProviderBudgetUnits {
			return ErrProviderContract
		}
		var history model.BillingPriceVersion
		if err := tx.First(&history, "version = ?", row.PriceVersion).Error; err != nil {
			return err
		}
		prices, err := restoreSnapshot(history.Version, history.Payload)
		if err != nil {
			return err
		}
		if _, ok := prices.price(name); !ok {
			return ErrPriceMissing
		}
		pre = &PreDeduct{prices: prices, reservation: &Reservation{id: row.ID, origin: name, prices: prices, providerCosts: policy, claim: &claim}}
		return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("state", model.IntentRunning).Error
	})
	if err != nil {
		return nil, err
	}
	return
}

// checkIntentReservation fences every financial write made by an executor.
// Ordinary requests cannot present an intent reservation without its capability.
func checkIntentReservation(tx *gorm.DB, pre *PreDeduct, dispatch bool) (*model.GenerationIntent, error) {
	var in model.GenerationIntent
	found := tx.Omit("ciphertext").Where("billing_id = ?", pre.BillingID()).Limit(1).Find(&in)
	if found.Error != nil {
		return nil, found.Error
	}
	if found.RowsAffected == 0 {
		if pre.reservation.claim != nil {
			return nil, model.ErrClaimLost
		}
		return nil, nil
	}
	claim := pre.reservation.claim
	if claim == nil {
		return nil, ErrStrictUnavailable
	}
	if in.ID != claim.ID || in.ClaimEpoch != claim.Epoch || in.ClaimHash == "" || in.ClaimHash != claimHash(*claim) {
		return nil, model.ErrClaimLost
	}
	if dispatch {
		now := time.Now().UnixMilli()
		if in.State != model.IntentRunning || in.CancelRequested || in.ClaimUntil <= now || in.Deadline <= now {
			return nil, model.ErrClaimLost
		}
	} else if in.State != model.IntentRunning && in.State != model.IntentFinalizing && in.State != model.IntentSucceeded && in.State != model.IntentFailed && in.State != model.IntentCancelled && in.State != model.IntentUncertain {
		return nil, model.ErrClaimLost
	}
	return &in, nil
}

func finishClaimIntent(tx *gorm.DB, in *model.GenerationIntent, financialState string, complete bool) error {
	if in == nil || in.State != model.IntentRunning {
		return nil
	}
	state := model.IntentUncertain
	if financialState == Settled {
		state = model.IntentFailed
		if complete {
			state = model.IntentFinalizing
		} else if in.CancelRequested {
			state = model.IntentCancelled
		}
	}
	return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("state", state).Error
}

// RejectGenerationClaim is also the final safety net for failures before the
// coordinator adopts the hold (invalid content, revoked identity, admission).
func (e *Engine) RejectGenerationClaim(ctx context.Context, claim model.GenerationClaim) error {
	if !e.StrictReady() {
		return ErrStrictUnavailable
	}
	return e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		if in.State != model.IntentClaimed && in.State != model.IntentRunning && in.State != model.IntentFinalizing {
			return nil
		}
		return releaseIntent(tx, &in, model.IntentFailed)
	})
}

// Unauthenticated content is not evidence for a financial release. Preserve
// both holds and make the failure auditable instead of deleting/replaying it.
func (e *Engine) QuarantineGenerationClaim(ctx context.Context, claim model.GenerationClaim) error {
	if !e.StrictReady() {
		return ErrStrictUnavailable
	}
	return e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		if in.State != model.IntentClaimed && in.State != model.IntentRunning && in.State != model.IntentFinalizing {
			return nil
		}
		var reservation model.BillingReservation
		if err := tx.First(&reservation, "id = ? AND user_id = ? AND token_id = ? AND origin_model = ?", in.BillingID, in.UserID, in.TokenID, in.Model).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BillingReservation{}).Where("id = ? AND state <> ?", reservation.ID, Settled).
			Updates(map[string]any{"state": Pending, "reason": "intent_content_unavailable"}).Error; err != nil {
			return err
		}
		return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"state": model.IntentUncertain, "claim_hash": "", "claim_until": 0}).Error
	})
}

// AttachGenerationOutput binds a bounded archive before any network attempt.
// It never grants another owner or a new claim access to old plaintext.
func (e *Engine) AttachGenerationOutput(ctx context.Context, claim model.GenerationClaim, id string) error {
	if !e.StrictReady() {
		return ErrStrictUnavailable
	}
	if !intentHex(id, 32) {
		return model.ErrIntentConflict
	}
	return e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		if in.State != model.IntentClaimed || in.ClaimUntil <= time.Now().UnixMilli() || in.CancelRequested || (in.OutputRecordID != "" && in.OutputRecordID != id) {
			return model.ErrClaimLost
		}
		var output model.GenerationRecord
		if err := tx.First(&output, "id = ? AND user_id = ? AND token_id = ?", id, in.UserID, in.TokenID).Error; err != nil {
			return err
		}
		if output.State != "open" || output.ExpiresAt < in.Deadline || output.Committed || output.NextSequence != 0 || output.StoredBytes != 0 {
			return model.ErrIntentUnsafe
		}
		return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("output_record_id", id).Error
	})
}

// A known financial outcome does not prove that output was durably sealed.
// Success is published only after both facts exist; a crash between them never
// makes the request replayable and does not charge it a second time.
func (e *Engine) CompleteGenerationOutput(ctx context.Context, claim model.GenerationClaim) error {
	if !e.StrictReady() {
		return ErrStrictUnavailable
	}
	return e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		in, err := readClaim(tx, claim)
		if err != nil {
			return err
		}
		if in.State != model.IntentFinalizing {
			return nil
		}
		var output model.GenerationRecord
		if err := tx.First(&output, "id = ? AND user_id = ? AND token_id = ?", in.OutputRecordID, in.UserID, in.TokenID).Error; err != nil {
			return err
		}
		if output.State != "sealed" || !output.Committed {
			return model.ErrIntentUnsafe
		}
		var row model.BillingReservation
		if err := tx.First(&row, "id = ? AND user_id = ? AND token_id = ? AND origin_model = ?", in.BillingID, in.UserID, in.TokenID, in.Model).Error; err != nil {
			return err
		}
		if row.State != Settled {
			return model.ErrIntentUnsafe
		}
		return tx.Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Updates(map[string]any{"state": model.IntentSucceeded, "claim_until": 0}).Error
	})
}
