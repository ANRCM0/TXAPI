package store

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

const responseDigestSetting = "responses.idempotency_digest_version"

func responseHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func responseIdentifier(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, ch := range value {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

// The no-op write obtains SQLite's writer lock before authority, scope or quota
// reads. It also serializes initial namespace binding across Store instances.
func responseNamespace(tx *gorm.DB, version string, initialize bool) error {
	if !responseHash(version) {
		return model.ErrResponseUnavailable
	}
	if err := tx.Model(&model.Setting{}).Where("key = ?", responseDigestSetting).
		Update("value", gorm.Expr("value")).Error; err != nil {
		return err
	}
	var background model.Setting
	if e := tx.Where("key = ?", backgroundDigestSetting).Limit(1).Find(&background).Error; e != nil {
		return e
	}
	if background.Value != "" && background.Value != version {
		return model.ErrResponseUnavailable
	}
	var setting model.Setting
	found := tx.Where("key = ?", responseDigestSetting).Limit(1).Find(&setting)
	if found.Error != nil {
		return found.Error
	}
	if found.RowsAffected == 1 {
		if setting.Value != version {
			return model.ErrResponseUnavailable
		}
		return nil
	}
	if !initialize {
		return model.ErrResponseUnavailable
	}
	var count int64
	if err := tx.Model(&model.ResponseRequest{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return model.ErrResponseUnavailable
	}
	if err := tx.Model(&model.ResponseHistory{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return model.ErrResponseUnavailable
	}
	return tx.Create(&model.Setting{Key: responseDigestSetting, Value: version}).Error
}

func (s *Store) InitializeResponseRequests(ctx context.Context, digestVersion string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return responseNamespace(tx, digestVersion, true)
	})
}

func validResponseIdentity(in model.ResponseRequest) bool {
	return responseHash(in.ID) && in.UserID != 0 && in.TokenID != 0 && in.Operation == "responses" &&
		responseHash(in.KeyHash) && responseHash(in.RequestHash) && responseHash(in.DigestVersion) &&
		strings.HasPrefix(in.ResponseID, "resp_") && len(in.ResponseID) > 5 && responseIdentifier(in.ResponseID, 70) &&
		len(in.Model) > 0 && len(in.Model) <= 256 && utf8.ValidString(in.Model) && in.CreatedAt > 0 &&
		in.ExpiresAt > in.CreatedAt && in.ExpiresAt-in.CreatedAt <= int64((7*24*time.Hour)/time.Millisecond)
}

func responseKnownTerminal(state string) bool {
	return state == model.ResponseCompleted || state == model.ResponseFailed || state == model.ResponseRejected
}

func (s *Store) BeginResponseRequest(ctx context.Context, in model.ResponseRequest, limits model.ResponseRequestLimits) (out model.ResponseRequest, fresh bool, err error) {
	if in.UserID == 0 || in.TokenID == 0 {
		return out, false, access.ErrUnauthorized
	}
	now := time.Now().UnixMilli()
	if !validResponseIdentity(in) || in.State != model.ResponseDispatching || in.CreatedAt > now || in.ExpiresAt <= now ||
		in.UpstreamResponseID != "" || in.NativeStatus != "" || in.OutputRecordID != "" || in.StatusCode != 0 || in.HTTPStatus != 0 || in.BillingID != "" || in.ErrorType != "" {
		return out, false, model.ErrResponseConflict
	}
	if limits.OwnerRecords < 1 || limits.InstanceRecords < limits.OwnerRecords || limits.InstanceRecords > 65536 {
		return out, false, model.ErrResponseCapacity
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := responseNamespace(tx, in.DigestVersion, false); err != nil {
			return err
		}
		view, err := resolveTokenView(tx.Where("id = ? AND user_id = ?", in.TokenID, in.UserID))
		if err != nil {
			return err
		}
		if view.Policy.Validate() != nil || !view.Policy.Allows(in.Model) {
			return access.ErrUnauthorized
		}
		var previous model.ResponseRequest
		found := tx.Where("user_id = ? AND token_id = ? AND operation = ? AND key_hash = ?", in.UserID, in.TokenID, in.Operation, in.KeyHash).Limit(1).Find(&previous)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 1 {
			if previous.ExpiresAt <= time.Now().UnixMilli() && responseKnownTerminal(previous.State) {
				if err := tx.Delete(&previous).Error; err != nil {
					return err
				}
			} else {
				if previous.RequestHash != in.RequestHash || previous.DigestVersion != in.DigestVersion || previous.Model != in.Model || previous.Stream != in.Stream || previous.Store != in.Store {
					return model.ErrResponseConflict
				}
				out = previous
				return nil
			}
		}
		for _, owner := range []bool{false, true} {
			query := tx.Model(&model.ResponseRequest{})
			maximum := limits.InstanceRecords
			if owner {
				query, maximum = query.Where("user_id = ?", in.UserID), limits.OwnerRecords
			}
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return err
			}
			if count >= maximum {
				return model.ErrResponseCapacity
			}
		}
		var background int64
		// An unresolved v13 namespace cannot safely be assumed unrelated. Keep
		// its owner's foreground admission closed until replay/backfill or expiry.
		if e := tx.Model(&model.BackgroundJob{}).Where("user_id = ? AND token_id = ? AND (namespace_hash = ? OR namespace_hash = '')", in.UserID, in.TokenID, in.KeyHash).Count(&background).Error; e != nil {
			return e
		}
		if background != 0 {
			return model.ErrResponseConflict
		}
		if err := noBackgroundResource(tx, in.ResponseID); err != nil {
			return err
		}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&in)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected != 1 {
			return model.ErrResponseConflict
		}
		out, fresh = in, true
		return nil
	})
	if err != nil {
		return model.ResponseRequest{}, false, err
	}
	return out, fresh, nil
}

func responseSameIdentity(a, b model.ResponseRequest) bool {
	return a.ID == b.ID && a.UserID == b.UserID && a.TokenID == b.TokenID && a.Operation == b.Operation &&
		a.KeyHash == b.KeyHash && a.RequestHash == b.RequestHash && a.DigestVersion == b.DigestVersion &&
		a.ResponseID == b.ResponseID && a.Model == b.Model && a.Stream == b.Stream && a.Store == b.Store &&
		a.CreatedAt == b.CreatedAt && a.ExpiresAt == b.ExpiresAt
}

func responseCompletion(in model.ResponseRequest) model.ResponseRequestCompletion {
	return model.ResponseRequestCompletion{State: in.State, NativeStatus: in.NativeStatus, UpstreamResponseID: in.UpstreamResponseID,
		OutputRecordID: in.OutputRecordID, StatusCode: in.StatusCode, HTTPStatus: in.HTTPStatus, BillingID: in.BillingID, ErrorType: in.ErrorType}
}

func validResponseCompletion(in model.ResponseRequestCompletion) bool {
	if !responseKnownTerminal(in.State) && in.State != model.ResponseUncertain {
		return false
	}
	switch in.NativeStatus {
	case "", "completed", "incomplete", "failed", "cancelled":
	default:
		return false
	}
	if !responseIdentifier(in.UpstreamResponseID, 256) || !responseIdentifier(in.BillingID, 64) || !responseIdentifier(in.ErrorType, 64) ||
		(in.OutputRecordID != "" && !responseHash(in.OutputRecordID)) ||
		(in.StatusCode != 0 && (in.StatusCode < 100 || in.StatusCode > 599)) || (in.HTTPStatus != 0 && (in.HTTPStatus < 100 || in.HTTPStatus > 599)) {
		return false
	}
	for _, ch := range in.ErrorType {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' && ch != '-' && ch != '.' {
			return false
		}
	}
	return true
}

func (s *Store) FinishResponseRequest(ctx context.Context, original model.ResponseRequest, completion model.ResponseRequestCompletion) error {
	if !validResponseIdentity(original) || !validResponseCompletion(completion) || (!original.Store && completion.OutputRecordID != "") {
		return model.ErrResponseConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := responseNamespace(tx, original.DigestVersion, false); err != nil {
			return err
		}
		var current model.ResponseRequest
		if err := tx.Where("id = ? AND user_id = ? AND token_id = ?", original.ID, original.UserID, original.TokenID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.ErrResponseConflict
			}
			return err
		}
		if !responseSameIdentity(current, original) {
			return model.ErrResponseConflict
		}
		if current.State != model.ResponseDispatching {
			if responseCompletion(current) == completion {
				return nil
			}
			return model.ErrResponseConflict
		}
		if completion.OutputRecordID != "" {
			var count int64
			if err := tx.Model(&model.GenerationRecord{}).Where("id = ? AND user_id = ? AND token_id = ? AND state = ? AND committed = ? AND expires_at > ?",
				completion.OutputRecordID, original.UserID, original.TokenID, "sealed", true, time.Now().UnixMilli()).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return model.ErrResponseConflict
			}
		}
		updated := tx.Model(&model.ResponseRequest{}).Where("id = ? AND state = ?", original.ID, model.ResponseDispatching).Updates(map[string]any{
			"state": completion.State, "native_status": completion.NativeStatus, "upstream_response_id": completion.UpstreamResponseID,
			"output_record_id": completion.OutputRecordID, "status_code": completion.StatusCode, "http_status": completion.HTTPStatus,
			"billing_id": completion.BillingID, "error_type": completion.ErrorType,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return model.ErrResponseConflict
		}
		return nil
	})
}

func (s *Store) RecoverResponseRequests(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, model.ErrResponseCapacity
	}
	var affected int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Model(&model.ResponseRequest{}).Where("state = ?", model.ResponseDispatching).Order("created_at, id").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Model(&model.ResponseRequest{}).Where("id IN ? AND state = ?", ids, model.ResponseDispatching).Update("state", model.ResponseUncertain)
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

func (s *Store) CleanupResponseRequests(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 1000 || now.IsZero() {
		return 0, model.ErrResponseCapacity
	}
	var affected int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		terminal := []string{model.ResponseCompleted, model.ResponseFailed, model.ResponseRejected}
		var ids []string
		if err := tx.Model(&model.ResponseRequest{}).Where("expires_at <= ? AND state IN ?", now.UnixMilli(), terminal).
			Order("expires_at, id").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Where("id IN ? AND expires_at <= ? AND state IN ?", ids, now.UnixMilli(), terminal).Delete(&model.ResponseRequest{})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}
