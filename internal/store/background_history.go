package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundHistoryRow(r state.ResponseHistory) model.ResponseHistory {
	return model.ResponseHistory{ID: r.ID, UserID: r.UserID, TokenID: r.TokenID, Kind: r.Kind, Model: r.Model, Version: r.Version, Busy: r.Busy, Deleted: r.Deleted, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, ReservedBytes: r.ReservedBytes, Ciphertext: r.Ciphertext}
}

func reserveBackgroundHistory(tx *gorm.DB, in bs.Record) error {
	reservation := in.HistoryReservation
	if reservation == nil {
		return nil
	}
	r, l := reservation.Row, reservation.Limits
	if !in.Store || r.ID != in.PublicID || r.UserID != in.UserID || r.TokenID != in.TokenID || r.Model != in.Model || r.Kind != "response" || r.Version != 1 || r.Busy != bs.HistoryBusy(in.ID) || r.Deleted || r.CreatedAt != in.CreatedAt || r.ExpiresAt != in.ContentExpiresAt || r.ReservedBytes != model.ResponseHistoryMaxBytes || len(r.Ciphertext) < 30 || len(r.Ciphertext) > model.ResponseHistoryMaxBytes {
		return bg.ErrConflict
	}
	if l.OwnerRecords < 1 || l.InstanceRecords < l.OwnerRecords || l.InstanceRecords > 65536 || l.OwnerBytes < model.ResponseHistoryMaxBytes || l.InstanceBytes < l.OwnerBytes || l.InstanceBytes > 1<<30 {
		return bg.ErrCapacity
	}
	row := backgroundHistoryRow(r)
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	for _, owner := range []bool{false, true} {
		q := tx.Model(&model.ResponseHistory{})
		maxRows, maxBytes := l.InstanceRecords, l.InstanceBytes
		if owner {
			q = q.Where("user_id = ?", in.UserID)
			maxRows, maxBytes = l.OwnerRecords, l.OwnerBytes
		}
		var usage struct{ N, B int64 }
		if err := q.Select("COUNT(*) AS n, COALESCE(SUM(reserved_bytes),0) AS b").Scan(&usage).Error; err != nil {
			return err
		}
		if usage.N > maxRows || usage.B > maxBytes {
			return bg.ErrCapacity
		}
	}
	return nil
}

func pendingBackgroundHistory(tx *gorm.DB, row model.BackgroundJob) *gorm.DB {
	return tx.Model(&model.ResponseHistory{}).Where("id = ? AND user_id = ? AND token_id = ? AND model = ? AND kind = 'response' AND version = 1 AND busy = ? AND reserved_bytes = ?", row.PublicID, row.UserID, row.TokenID, row.Model, bs.HistoryBusy(row.ID), model.ResponseHistoryMaxBytes).
		Where("EXISTS (SELECT 1 FROM background_jobs b WHERE b.id = ? AND b.store = 1)", row.ID)
}

// A tombstone releases pending content while preserving publication CAS. An
// expired, revoked or deleted reservation can never be recreated by recovery.
func releaseBackgroundHistory(tx *gorm.DB, row model.BackgroundJob) error {
	return pendingBackgroundHistory(tx, row).Updates(map[string]any{"version": 2, "busy": "", "deleted": true, "reserved_bytes": 0, "ciphertext": []byte{}}).Error
}

func updateBackgroundHistory(tx *gorm.DB, row model.BackgroundJob, in bs.Update) error {
	if in.History == nil {
		if in.Stage == bg.StageTerminal || in.Stage == bg.StageUncertain {
			return releaseBackgroundHistory(tx, row)
		}
		return nil
	}
	if !row.Store || in.Stage != bg.StageTerminal || in.UsageMetadata == nil || in.UsageMetadata.NativeState != bg.RemoteSucceeded {
		return bg.ErrConflict
	}
	change := in.History
	r := change.Record
	now := time.Now().UnixMilli()
	if change.Expected != 1 || r.ID != row.PublicID || r.UserID != row.UserID || r.TokenID != row.TokenID || r.Kind != "response" || r.Model != row.Model || r.Version != 2 || r.Busy != "" || r.Deleted || r.CreatedAt < row.CreatedAt || r.CreatedAt > now || r.ExpiresAt <= now || r.ExpiresAt <= r.CreatedAt || r.ExpiresAt-r.CreatedAt > int64(7*24*time.Hour/time.Millisecond) || len(r.Ciphertext) < 30 || len(r.Ciphertext) > model.ResponseHistoryMaxBytes || r.ReservedBytes < int64(len(r.Ciphertext)) || r.ReservedBytes > model.ResponseHistoryMaxBytes {
		return bg.ErrConflict
	}
	var old model.ResponseHistory
	q := pendingBackgroundHistory(tx, row).Limit(1).Find(&old)
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected == 0 {
		return nil
	}
	if old.Deleted || old.ExpiresAt <= now || old.CreatedAt != row.CreatedAt || old.ExpiresAt != row.ContentExpiresAt {
		return releaseBackgroundHistory(tx, row)
	}
	if err := backgroundAuthority(tx, identity.Binding{UserID: row.UserID, TokenID: row.TokenID}, row.Model); err != nil {
		if errors.Is(err, bg.ErrNotFound) {
			return releaseBackgroundHistory(tx, row)
		}
		return err
	}
	// This private reservation-to-publication transition alone resets history
	// creation/retention. Ordinary history CAS continues to forbid TTL changes.
	return pendingBackgroundHistory(tx, row).Where("deleted = 0 AND expires_at > ?", now).Updates(map[string]any{"version": r.Version, "busy": "", "created_at": r.CreatedAt, "expires_at": r.ExpiresAt, "reserved_bytes": r.ReservedBytes, "ciphertext": r.Ciphertext}).Error
}

func releaseInactiveBackgroundHistory(tx *gorm.DB) error {
	return tx.Model(&model.ResponseHistory{}).Where(`version = 1 AND kind = 'response' AND EXISTS (
		SELECT 1 FROM background_jobs b WHERE b.public_id = response_histories.id
		AND b.user_id = response_histories.user_id AND b.token_id = response_histories.token_id
		AND b.model = response_histories.model AND b.store = 1
		AND response_histories.created_at = b.created_at AND response_histories.expires_at = b.content_expires_at
		AND response_histories.reserved_bytes = ? AND response_histories.busy = 'bg_' || substr(b.id,1,32)
		AND (b.stage IN ('terminal','uncertain') OR b.deleted = 1 OR b.content_expires_at <= ?))`, model.ResponseHistoryMaxBytes, time.Now().UnixMilli()).Updates(map[string]any{"version": 2, "busy": "", "deleted": true, "reserved_bytes": 0, "ciphertext": []byte{}}).Error
}
