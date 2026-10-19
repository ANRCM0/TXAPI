package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"minigateway/internal/access"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	facts "minigateway/internal/foundation/contracts/execution/v2"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/model"
)

type usageRecorder struct {
	store  *Store
	limits model.UsageHistoryLimits
}

func (s *Store) UsageRecorder(limits model.UsageHistoryLimits) history.Recorder {
	return &usageRecorder{s, limits}
}

func usageLimitsValid(l model.UsageHistoryLimits) bool {
	return l.InstanceRecords >= 1 && l.InstanceRecords <= 65536 && l.OwnerRecords >= 1 && l.OwnerRecords <= l.InstanceRecords && l.RetentionSeconds >= 1 && l.RetentionSeconds <= 2592000
}

func usageID(id string) bool {
	if len(id) != 38 || !strings.HasPrefix(id, "usage_") {
		return false
	}
	b, err := hex.DecodeString(id[6:])
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id[6:]
}

func usageText(s string, max int, optional bool) bool {
	if len(s) > max || !utf8.ValidString(s) || !optional && s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func usageDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func usageHostValid(h history.HostFacts) bool {
	if h.BytesWritten < 0 {
		return false
	}
	for _, status := range []int{h.Status, h.UpstreamStatus, h.HTTPStatus} {
		if status != 0 && (status < 100 || status > 599) {
			return false
		}
	}
	switch h.Phase {
	case "", execution.NotDispatched, execution.Dispatching, execution.ResponseReceived:
	default:
		return false
	}
	switch h.Termination {
	case "", execution.Success, execution.UpstreamError, execution.Deadline, execution.Canceled, execution.ClientWriteError:
	default:
		return false
	}
	return true
}

func usageContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, history.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	return c, cancel, nil
}

func usageError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{history.ErrInvalid, history.ErrUnauthorized, history.ErrForbidden, history.ErrNotFound, history.ErrConflict, history.ErrCapacity, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return history.ErrUnavailable
}

func usageBinding(ctx context.Context) (access.Principal, error) {
	p, ok := access.PrincipalFromContext(ctx)
	if !ok || p.UserID == 0 || p.TokenID == 0 {
		return p, history.ErrUnauthorized
	}
	return p, nil
}

// The write lock is acquired before authorization, capacity and replay reads,
// including when another Store points at the same SQLite file.
func usageWriteLock(tx *gorm.DB) error {
	return tx.Exec("UPDATE schema_migrations SET version = version WHERE version = 16").Error
}

func usageParent(tx *gorm.DB, p access.Principal, id string) (row model.UsageInvocation, err error) {
	res := tx.Where("id = ? AND user_id = ? AND token_id = ?", id, p.UserID, p.TokenID).Limit(1).Find(&row)
	if res.Error != nil {
		return row, res.Error
	}
	if res.RowsAffected != 1 {
		return row, history.ErrNotFound
	}
	return row, nil
}

func (r *usageRecorder) write(ctx context.Context, id string, fn func(*gorm.DB, access.Principal) error) error {
	c, cancel, err := usageContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if !usageID(id) {
		return history.ErrInvalid
	}
	p, err := usageBinding(c)
	if err != nil {
		return err
	}
	err = r.store.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := usageWriteLock(tx); err != nil {
			return err
		}
		return fn(tx, p)
	})
	return usageError(err)
}

func (r *usageRecorder) Begin(ctx context.Context, in history.Start) error {
	if !usageLimitsValid(r.limits) || !usageID(in.ID) || !usageText(in.RequestID, 64, true) || !usageText(in.Model, 256, false) || !usageText(string(in.Operation), 32, false) || in.MaxAttempts < 1 || in.MaxAttempts > 11 || in.QuotationVersion != "" && !quotationVersionID(in.QuotationVersion) {
		return history.ErrInvalid
	}
	return r.write(ctx, in.ID, func(tx *gorm.DB, p access.Principal) error {
		view, err := resolveTokenView(tx.Where("id = ? AND user_id = ?", p.TokenID, p.UserID))
		if err != nil {
			if errors.Is(err, access.ErrUnauthorized) {
				return history.ErrUnauthorized
			}
			return err
		}
		if !view.Policy.Allows(in.Model) {
			return history.ErrForbidden
		}
		fingerprint := usageDigest(struct {
			UserID, TokenID uint
			Start           history.Start
		}{p.UserID, p.TokenID, in})
		var old model.UsageInvocation
		res := tx.Where("id = ?", in.ID).Limit(1).Find(&old)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 0 {
			if old.UserID == p.UserID && old.TokenID == p.TokenID && old.StartFingerprint == fingerprint {
				return nil
			}
			return history.ErrConflict
		}
		if _, err := cleanupUsageHistory(tx, time.Now().UnixMilli(), 1000); err != nil {
			return err
		}
		for _, owner := range []bool{false, true} {
			q := tx.Model(&model.UsageInvocation{})
			bound := r.limits.InstanceRecords
			if owner {
				q = q.Where("user_id = ?", p.UserID)
				bound = r.limits.OwnerRecords
			}
			var count int64
			if err := q.Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(bound) {
				return history.ErrCapacity
			}
		}
		now := time.Now().UnixMilli()
		return tx.Create(&model.UsageInvocation{ID: in.ID, UserID: p.UserID, TokenID: p.TokenID, RequestID: in.RequestID, Operation: string(in.Operation), Model: in.Model, Stream: in.Stream, QuotationVersion: in.QuotationVersion, MaxAttempts: in.MaxAttempts, State: "open", CreatedAt: now, ExpiresAt: now + int64(r.limits.RetentionSeconds)*1000, Host: history.HostFacts{Phase: execution.NotDispatched}, StartFingerprint: fingerprint}).Error
	})
}

func quotationVersionID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

func (r *usageRecorder) Prepare(ctx context.Context, in history.Target) error {
	if in.Number < 1 || in.Number > 11 || in.ChannelID == 0 || in.CredentialID == 0 || !usageText(in.ActualModel, 256, false) || !usageText(in.Group, 128, false) || !usageText(in.Protocol, 64, false) {
		return history.ErrInvalid
	}
	return r.write(ctx, in.ID, func(tx *gorm.DB, p access.Principal) error {
		parent, e := usageParent(tx, p, in.ID)
		if e != nil {
			return e
		}
		if in.Number > parent.MaxAttempts {
			return history.ErrConflict
		}
		var old model.UsageAttemptReceipt
		res := tx.Where("invocation_id = ? AND number = ?", in.ID, in.Number).Limit(1).Find(&old)
		if res.Error != nil {
			return res.Error
		}
		fingerprint := usageDigest(in)
		if res.RowsAffected != 0 {
			if old.PrepareFingerprint == fingerprint {
				return nil
			}
			return history.ErrConflict
		}
		if parent.State != "open" || in.Number != parent.AttemptCount+1 {
			return history.ErrConflict
		}
		if e := tx.Create(&model.UsageAttemptReceipt{InvocationID: in.ID, Number: in.Number, ChannelID: in.ChannelID, CredentialID: in.CredentialID, ActualModel: in.ActualModel, Group: in.Group, Protocol: in.Protocol, State: "prepared", Host: history.HostFacts{Phase: execution.NotDispatched}, Completeness: string(facts.Unknown), Source: string(facts.SourceUnknown), PrepareFingerprint: fingerprint}).Error; e != nil {
			return e
		}
		return tx.Model(&parent).Update("attempt_count", parent.AttemptCount+1).Error
	})
}

func usageAttempt(tx *gorm.DB, id string, number int) (row model.UsageAttemptReceipt, err error) {
	res := tx.Where("invocation_id = ? AND number = ?", id, number).Limit(1).Find(&row)
	if res.Error != nil {
		return row, res.Error
	}
	if res.RowsAffected != 1 {
		return row, history.ErrNotFound
	}
	return row, nil
}

func (r *usageRecorder) MayDispatch(ctx context.Context, id string, number int) error {
	if number < 1 || number > 11 {
		return history.ErrInvalid
	}
	return r.write(ctx, id, func(tx *gorm.DB, p access.Principal) error {
		parent, e := usageParent(tx, p, id)
		if e != nil {
			return e
		}
		row, e := usageAttempt(tx, id, number)
		if e != nil {
			return e
		}
		if parent.State != "open" || row.State != "prepared" && row.State != "may_dispatch" {
			return history.ErrConflict
		}
		if e := tx.Model(&row).Updates(map[string]any{"state": "may_dispatch", "may_dispatch": true, "host_phase": execution.Dispatching}).Error; e != nil {
			return e
		}
		return tx.Model(&parent).Update("host_phase", execution.Dispatching).Error
	})
}

func (r *usageRecorder) Observe(ctx context.Context, in history.Receipt) error {
	if in.Number < 1 || in.Number > 11 || !usageHostValid(in.Host) || in.Usage.Validate() != nil {
		return history.ErrInvalid
	}
	return r.write(ctx, in.ID, func(tx *gorm.DB, p access.Principal) error {
		parent, e := usageParent(tx, p, in.ID)
		if e != nil {
			return e
		}
		row, e := usageAttempt(tx, in.ID, in.Number)
		if e != nil {
			return e
		}
		fingerprint := usageDigest(in)
		if row.FinishFingerprint != "" {
			if row.FinishFingerprint == fingerprint {
				return nil
			}
			return history.ErrConflict
		}
		if parent.State != "open" || row.State != "prepared" && row.State != "may_dispatch" {
			return history.ErrConflict
		}
		if !row.MayDispatch && in.Host.Phase != "" && in.Host.Phase != execution.NotDispatched {
			return history.ErrConflict
		}
		row.Host = in.Host
		row.State = "observed"
		row.FinishFingerprint = fingerprint
		row.InputTokens = in.Usage.Counts.InputTokens
		row.OutputTokens = in.Usage.Counts.OutputTokens
		row.TotalTokens = in.Usage.Counts.TotalTokens
		row.CachedInputTokens = in.Usage.Breakdown.CachedInputTokens
		row.CacheWriteInputTokens = in.Usage.Breakdown.CacheWriteInputTokens
		row.ReasoningOutputTokens = in.Usage.Breakdown.ReasoningOutputTokens
		row.Completeness = string(in.Usage.Completeness)
		row.Source = string(in.Usage.Source)
		row.Profile = in.Usage.Profile
		return tx.Save(&row).Error
	})
}

func (r *usageRecorder) Finish(ctx context.Context, in history.Completion) error {
	if !usageHostValid(in.Host) {
		return history.ErrInvalid
	}
	return r.write(ctx, in.ID, func(tx *gorm.DB, p access.Principal) error {
		parent, e := usageParent(tx, p, in.ID)
		if e != nil {
			return e
		}
		fingerprint := usageDigest(in)
		if parent.FinishFingerprint != "" {
			if parent.FinishFingerprint == fingerprint {
				return nil
			}
			return history.ErrConflict
		}
		if parent.State != "open" {
			return history.ErrConflict
		}
		var counts struct{ Total, Pending int64 }
		if e := tx.Model(&model.UsageAttemptReceipt{}).Where("invocation_id = ?", in.ID).
			Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN state <> 'observed' THEN 1 ELSE 0 END),0) AS pending").Scan(&counts).Error; e != nil {
			return e
		}
		if counts.Total != int64(parent.AttemptCount) || !in.Interrupted && counts.Pending != 0 {
			return history.ErrConflict
		}
		parent.State = "sealed"
		if in.Interrupted {
			parent.State = "interrupted"
			if e := tx.Model(&model.UsageAttemptReceipt{}).Where("invocation_id = ? AND state IN ?", in.ID, []string{"prepared", "may_dispatch"}).Update("state", "interrupted").Error; e != nil {
				return e
			}
		}
		parent.Host = in.Host
		parent.FinishedAt = time.Now().UnixMilli()
		parent.FinishFingerprint = fingerprint
		return tx.Save(&parent).Error
	})
}

func cleanupUsageHistory(tx *gorm.DB, now int64, limit int) (int, error) {
	var ids []string
	if e := tx.Model(&model.UsageInvocation{}).Where("expires_at <= ? AND state IN ?", now, []string{"sealed", "interrupted"}).Order("expires_at,id").Limit(limit).Pluck("id", &ids).Error; e != nil {
		return 0, e
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if e := tx.Where("invocation_id IN ?", ids).Delete(&model.UsageAttemptReceipt{}).Error; e != nil {
		return 0, e
	}
	res := tx.Where("id IN ?", ids).Delete(&model.UsageInvocation{})
	return int(res.RowsAffected), res.Error
}

func (s *Store) CleanupUsageHistory(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, history.ErrInvalid
	}
	c, cancel, e := usageContext(ctx)
	if e != nil {
		return 0, e
	}
	defer cancel()
	var n int
	e = s.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if e := usageWriteLock(tx); e != nil {
			return e
		}
		var err error
		n, err = cleanupUsageHistory(tx, time.Now().UnixMilli(), limit)
		return err
	})
	if e != nil {
		n = 0
	}
	return n, usageError(e)
}

func (s *Store) InterruptUsageHistory(ctx context.Context) error {
	c, cancel, e := usageContext(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	e = s.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if e := usageWriteLock(tx); e != nil {
			return e
		}
		open := tx.Model(&model.UsageInvocation{}).Select("id").Where("state = ?", "open")
		if e := tx.Model(&model.UsageAttemptReceipt{}).Where("invocation_id IN (?) AND state IN ?", open, []string{"prepared", "may_dispatch"}).Update("state", "interrupted").Error; e != nil {
			return e
		}
		return tx.Model(&model.UsageInvocation{}).Where("state = ?", "open").Updates(map[string]any{"state": "interrupted", "finished_at": time.Now().UnixMilli()}).Error
	})
	return usageError(e)
}

var _ history.Recorder = (*usageRecorder)(nil)
