package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

var (
	ErrArchiveCapacity = errors.New("generation archive capacity exceeded")
	ErrArchiveConflict = errors.New("generation archive is expired, closed or changed")
)

func (s *Store) CreateGenerationRecord(ctx context.Context, record model.GenerationRecord, limits model.GenerationArchiveLimits) error {
	now := time.Now().UnixMilli()
	if len(record.ID) != 64 || record.UserID == 0 || record.TokenID == 0 || record.State != "open" ||
		record.MaxBytes < 16384 || record.MaxBytes > 8<<20 || record.StoredBytes != 0 || record.NextSequence != 0 || record.Committed ||
		record.CreatedAt <= 0 || record.CreatedAt > now || record.ExpiresAt <= now || record.ExpiresAt-record.CreatedAt > int64((7*24*time.Hour)/time.Millisecond) ||
		limits.InstanceBytes < record.MaxBytes || limits.OwnerBytes < record.MaxBytes || limits.InstanceRecords < 1 || limits.OwnerRecords < 1 {
		return ErrArchiveCapacity
	}
	// The singleton SQLite connection serializes quota-check + reservation. Even
	// expired records keep their reservation until bounded cleanup deletes them.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, owner := range []bool{false, true} {
			q := tx.Model(&model.GenerationRecord{})
			maxBytes, maxCount := limits.InstanceBytes, limits.InstanceRecords
			if owner {
				q = q.Where("user_id = ?", record.UserID)
				maxBytes, maxCount = limits.OwnerBytes, limits.OwnerRecords
			}
			var usage struct{ Bytes, Count int64 }
			if err := q.Select("COALESCE(SUM(max_bytes),0) AS bytes, COUNT(*) AS count").Scan(&usage).Error; err != nil {
				return err
			}
			if usage.Bytes > maxBytes-record.MaxBytes || usage.Count >= maxCount {
				return ErrArchiveCapacity
			}
		}
		record.Segments = nil
		return tx.Create(&record).Error
	})
}

func (s *Store) AppendGenerationSegment(ctx context.Context, record model.GenerationRecord, segment model.GenerationSegment) error {
	if segment.RecordID != record.ID || segment.Sequence != record.NextSequence || len(segment.Ciphertext) < 30 || len(segment.Ciphertext) > 65536 ||
		segment.Sequence >= 256 || (segment.Kind != "metadata" && segment.Kind != "body" && segment.Kind != "result") {
		return ErrArchiveConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.GenerationRecord{}).Where("id = ? AND user_id = ? AND token_id = ? AND state = ? AND next_sequence = ? AND expires_at > ? AND stored_bytes <= max_bytes - ?",
			record.ID, record.UserID, record.TokenID, "open", record.NextSequence, time.Now().UnixMilli(), len(segment.Ciphertext))
		if segment.Kind == "metadata" {
			q = q.Where("next_sequence = 0 AND committed = ?", false)
		} else if segment.Kind == "body" {
			q = q.Where("committed = ? AND next_sequence < 255", true)
		}
		updates := map[string]any{"stored_bytes": gorm.Expr("stored_bytes + ?", len(segment.Ciphertext)), "next_sequence": gorm.Expr("next_sequence + 1")}
		if segment.Kind == "metadata" {
			updates["committed"] = true
		}
		if segment.Kind == "result" {
			updates["state"] = "sealed"
		}
		changed := q.Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return ErrArchiveConflict
		}
		return tx.Create(&segment).Error
	})
}

// ReadGenerationRecord returns one coherent snapshot; authority is checked by
// the consumer before this binding-scoped read, never by a caller-provided ID.
func (s *Store) ReadGenerationRecord(ctx context.Context, binding access.Binding, id string) (record model.GenerationRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ? AND token_id = ? AND expires_at > ?", id, binding.UserID, binding.TokenID, time.Now().UnixMilli()).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Metadata is checked before loading blobs, including on a damaged DB.
		if record.MaxBytes < 16384 || record.MaxBytes > 8<<20 || record.StoredBytes < 0 || record.StoredBytes > record.MaxBytes || record.NextSequence > 256 {
			return ErrArchiveConflict
		}
		var segments struct{ Count, Bytes, Largest int64 }
		if err := tx.Model(&model.GenerationSegment{}).Where("record_id = ?", id).
			Select("COUNT(*) AS count, COALESCE(SUM(length(ciphertext)),0) AS bytes, COALESCE(MAX(length(ciphertext)),0) AS largest").Scan(&segments).Error; err != nil {
			return err
		}
		if segments.Count != int64(record.NextSequence) || segments.Bytes != record.StoredBytes || segments.Largest > 65536 {
			return ErrArchiveConflict
		}
		return tx.Where("record_id = ?", id).Order("sequence ASC").Limit(257).Find(&record.Segments).Error
	})
	return
}

func (s *Store) DeleteGenerationRecord(ctx context.Context, binding access.Binding, id string) error {
	result := s.db.WithContext(ctx).Where("id = ? AND user_id = ? AND token_id = ?", id, binding.UserID, binding.TokenID).Delete(&model.GenerationRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

// InterruptGenerationRecords is called only during exclusive startup, before
// starting any producer. It records incomplete observation, not zero cost or
// eligibility for replay. It does not change billing or return executable work.
func (s *Store) InterruptGenerationRecords(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrArchiveConflict
	}
	q := s.db.Model(&model.GenerationRecord{}).Select("id").Where("state = ?", "open").
		Where("NOT EXISTS (SELECT 1 FROM generation_intents gi WHERE gi.output_record_id = generation_records.id AND gi.state IN ? AND gi.claim_until > ?)",
			[]string{model.IntentClaimed, model.IntentRunning, model.IntentFinalizing}, time.Now().UnixMilli()).Order("created_at, id").Limit(limit)
	result := s.db.WithContext(ctx).Model(&model.GenerationRecord{}).Where("id IN (?) AND state = ?", q, "open").Update("state", "interrupted")
	return result.RowsAffected, result.Error
}

func (s *Store) CleanupGenerationRecords(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrArchiveConflict
	}
	q := s.db.Model(&model.GenerationRecord{}).Select("id").Where("expires_at <= ?", time.Now().UnixMilli()).Order("expires_at, id").Limit(limit)
	result := s.db.WithContext(ctx).Where("id IN (?)", q).Delete(&model.GenerationRecord{})
	return result.RowsAffected, result.Error
}
