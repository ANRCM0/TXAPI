package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

func historyAuthority(tx *gorm.DB, binding access.Binding, name string) error {
	view, err := resolveTokenView(tx.Where("id = ? AND user_id = ?", binding.TokenID, binding.UserID))
	if err != nil {
		return err
	}
	if view.Policy.Validate() != nil || name != "" && !view.Policy.Allows(name) {
		return model.ErrHistoryNotFound
	}
	return nil
}

func historyRead(tx *gorm.DB, binding access.Binding, id string) (row model.ResponseHistory, err error) {
	// Inspect size before loading content, including corrupted SQLite rows.
	if len(id) > 80 || binding.UserID == 0 || binding.TokenID == 0 {
		return row, model.ErrHistoryNotFound
	}
	q := tx.Where("id = ? AND user_id = ? AND token_id = ? AND deleted = ? AND expires_at > ? AND length(ciphertext) <= ?", id, binding.UserID, binding.TokenID, false, time.Now().UnixMilli(), model.ResponseHistoryMaxBytes)
	if err = q.First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return row, model.ErrHistoryNotFound
	}
	if err != nil {
		return row, err
	}
	if err = historyAuthority(tx, binding, row.Model); err != nil {
		return model.ResponseHistory{}, err
	}
	return row, nil
}

func (s *Store) ReadResponseHistory(ctx context.Context, binding access.Binding, id string) (row model.ResponseHistory, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var readErr error
		row, readErr = historyRead(tx, binding, id)
		return readErr
	})
	return
}

// ApplyResponseHistory atomically verifies all referenced revisions and writes
// at most a response and its conversation. It never holds a lock during I/O.
func (s *Store) ApplyResponseHistory(ctx context.Context, binding access.Binding, changes []model.HistoryChange, guards []model.HistoryGuard, limits model.HistoryLimits) error {
	if len(changes) < 1 || len(changes) > 2 || len(guards) > 2 || limits.OwnerRecords < 1 || limits.InstanceRecords < limits.OwnerRecords || limits.InstanceRecords > 65536 || limits.OwnerBytes < model.ResponseHistoryMaxBytes || limits.InstanceBytes < limits.OwnerBytes || limits.InstanceBytes > 1<<30 {
		return model.ErrHistoryCapacity
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Setting{}).Where("key = ?", responseDigestSetting).Update("value", gorm.Expr("value")).Error; err != nil {
			return err
		}
		if err := historyAuthority(tx, binding, ""); err != nil {
			return err
		}
		for _, guard := range guards {
			row, err := historyRead(tx, binding, guard.ID)
			if err != nil {
				return err
			}
			if row.Version != guard.Version || row.Busy != "" {
				return model.ErrHistoryConflict
			}
		}
		seen := map[string]bool{}
		for _, change := range changes {
			r := change.Record
			if seen[r.ID] || !responseIdentifier(r.ID, 80) || len(r.ID) < 6 || r.UserID != binding.UserID || r.TokenID != binding.TokenID || r.Version != change.Expected+1 || r.Version == 0 || r.CreatedAt <= 0 || r.ExpiresAt <= time.Now().UnixMilli() || r.ExpiresAt <= r.CreatedAt || r.ExpiresAt-r.CreatedAt > int64(7*24*time.Hour/time.Millisecond) || r.ReservedBytes < int64(len(r.Ciphertext)) || r.ReservedBytes > model.ResponseHistoryMaxBytes || r.ReservedBytes < 0 || len(r.Model) > 256 || !responseIdentifier(r.Busy, 64) || r.Kind != "response" && r.Kind != "conversation" || r.Kind == "response" && !strings.HasPrefix(r.ID, "resp_") || r.Kind == "conversation" && !strings.HasPrefix(r.ID, "conv_") || r.Deleted && (r.Busy != "" || r.Kind == "response" && (len(r.Ciphertext) != 0 || r.ReservedBytes != 0)) {
				return model.ErrHistoryConflict
			}
			seen[r.ID] = true
			if err := historyAuthority(tx, binding, r.Model); err != nil {
				return err
			}
			if change.Expected != 0 {
				old, err := historyRead(tx, binding, r.ID)
				if err != nil {
					return err
				}
				if old.Version != change.Expected || old.CreatedAt != r.CreatedAt || old.ExpiresAt != r.ExpiresAt || old.Kind != r.Kind || old.Model != "" && old.Model != r.Model {
					return model.ErrHistoryConflict
				}
				if r.Deleted && r.Kind == "response" {
					var receipt model.ResponseRequest
					found := tx.Where("response_id = ? AND user_id = ? AND token_id = ?", r.ID, binding.UserID, binding.TokenID).Limit(1).Find(&receipt)
					if found.Error != nil {
						return found.Error
					}
					if found.RowsAffected != 0 && !responseKnownTerminal(receipt.State) {
						return model.ErrHistoryConflict
					}
					if receipt.OutputRecordID != "" {
						if err := tx.Where("id = ? AND user_id = ? AND token_id = ?", receipt.OutputRecordID, binding.UserID, binding.TokenID).Delete(&model.GenerationRecord{}).Error; err != nil {
							return err
						}
						if err := tx.Model(&receipt).Update("output_record_id", "").Error; err != nil {
							return err
						}
					}
				}
				updated := tx.Model(&model.ResponseHistory{}).Where("id = ? AND version = ?", r.ID, change.Expected).Select("model", "version", "busy", "deleted", "reserved_bytes", "ciphertext").Updates(&r)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return model.ErrHistoryConflict
				}
			} else {
				inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
				if inserted.Error != nil {
					return inserted.Error
				}
				if inserted.RowsAffected != 1 {
					return model.ErrHistoryConflict
				}
			}
		}
		for _, owner := range []bool{false, true} {
			q := tx.Model(&model.ResponseHistory{})
			maxRows, maxBytes := limits.InstanceRecords, limits.InstanceBytes
			if owner {
				q = q.Where("user_id = ?", binding.UserID)
				maxRows, maxBytes = limits.OwnerRecords, limits.OwnerBytes
			}
			var sum struct {
				N int64
				B int64
			}
			if err := q.Select("COUNT(*) AS n, COALESCE(SUM(reserved_bytes),0) AS b").Scan(&sum).Error; err != nil {
				return err
			}
			if sum.N > maxRows || sum.B > maxBytes {
				return model.ErrHistoryCapacity
			}
		}
		return nil
	})
}

func (s *Store) RecoverResponseHistory(ctx context.Context) error {
	// An interrupted conversation is blocked until explicit deletion: never append
	// a new turn over a generation whose outcome may be unknown.
	return s.db.WithContext(ctx).Model(&model.ResponseHistory{}).Where("busy <> '' AND busy <> ?", "uncertain").Update("busy", "uncertain").Error
}

func (s *Store) CleanupResponseHistory(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 200 {
		return 0, model.ErrHistoryCapacity
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&model.ResponseHistory{}).Where("expires_at <= ?", time.Now().UnixMilli()).Order("expires_at,id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	r := s.db.WithContext(ctx).Where("id IN ? AND expires_at <= ?", ids, time.Now().UnixMilli()).Delete(&model.ResponseHistory{})
	return r.RowsAffected, r.Error
}
