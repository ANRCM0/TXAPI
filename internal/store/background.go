package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

const backgroundDigestSetting = "background.digest_version"

func backgroundLock(tx *gorm.DB) error {
	// Serialize all authorization, quota and CAS reads across Store instances.
	return tx.Model(&model.Setting{}).Where("key = ?", backgroundDigestSetting).Update("value", gorm.Expr("value")).Error
}

func (s *Store) InitializeBackground(ctx context.Context, digest, mode string) (records []bs.Record, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backgroundLock(tx); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.BackgroundJob{}).Where("stage <> ?", bg.StageTerminal).Count(&count).Error; err != nil {
			return err
		}
		if mode != "disabled" {
			if count != 0 {
				return bg.ErrUnsupportedBilling
			}
			return nil
		}
		if !responseHash(digest) {
			return bg.ErrUnavailable
		}
		var foreground model.Setting
		if e := tx.Where("key = ?", responseDigestSetting).Limit(1).Find(&foreground).Error; e != nil {
			return e
		}
		if foreground.Value != "" && foreground.Value != digest {
			return bg.ErrUnavailable
		}
		var setting model.Setting
		found := tx.Where("key = ?", backgroundDigestSetting).Limit(1).Find(&setting)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected != 0 && setting.Value != digest {
			return bg.ErrUnavailable
		}
		if found.RowsAffected == 0 {
			if err := tx.Model(&model.BackgroundJob{}).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return bg.ErrUnavailable
			}
			if err := tx.Create(&model.Setting{Key: backgroundDigestSetting, Value: digest}).Error; err != nil {
				return err
			}
		}
		var all, valid int64
		if err := tx.Model(&model.BackgroundJob{}).Count(&all).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackgroundJob{}).Where("length(ciphertext) + length(snapshot) <= ?", bs.MaxInput+bs.MaxContent+512).Count(&valid).Error; err != nil {
			return err
		}
		if all != valid || all > 4096 {
			return bg.ErrUnavailable
		}
		var rows []model.BackgroundJob
		if err := tx.Where("length(ciphertext) + length(snapshot) <= ?", bs.MaxInput+bs.MaxContent+512).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			records = append(records, backgroundSnapshot(row))
		}
		return nil
	})
	return
}

func noBackgroundResource(tx *gorm.DB, id string) error {
	var count int64
	if e := tx.Model(&model.BackgroundJob{}).Where("public_id = ?", id).Count(&count).Error; e != nil {
		return e
	}
	if count != 0 {
		return model.ErrResponseConflict
	}
	return nil
}

func validBackgroundID(id string) bool {
	return len(id) == 69 && strings.HasPrefix(id, "resp_") && responseHash(id[5:])
}

func validBackgroundLimits(l bs.Limits) bool {
	return l.OwnerRecords >= 1 && l.InstanceRecords >= l.OwnerRecords && l.InstanceRecords <= 4096 &&
		l.OwnerBytes >= bs.MaxInput+256 && l.InstanceBytes >= l.OwnerBytes && l.InstanceBytes <= 1<<30 &&
		l.OwnerActive >= 1 && l.InstanceActive >= l.OwnerActive && l.InstanceActive <= l.InstanceRecords && l.OwnerActive <= l.OwnerRecords
}

func backgroundAuthority(tx *gorm.DB, binding identity.Binding, modelName string) error {
	err := historyAuthority(tx, binding, modelName)
	if errors.Is(err, access.ErrUnauthorized) || errors.Is(err, model.ErrHistoryNotFound) {
		return bg.ErrNotFound
	}
	return err
}

func backgroundResourceFree(tx *gorm.DB, id string) error {
	for _, table := range []interface{}{&model.ResponseHistory{}, &model.BackgroundJob{}} {
		var n int64
		column := "id"
		if _, ok := table.(*model.BackgroundJob); ok {
			column = "public_id"
		}
		if err := tx.Model(table).Where(column+" = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return bg.ErrConflict
		}
	}
	var n int64
	if err := tx.Model(&model.ResponseRequest{}).Where("response_id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return bg.ErrConflict
	}
	return nil
}

func (s *Store) AdmitBackground(ctx context.Context, in bs.Record, limits bs.Limits) (out bs.Record, err error) {
	now := time.Now().UnixMilli()
	if !responseHash(in.ID) || !validBackgroundID(in.PublicID) || in.UserID == 0 || in.TokenID == 0 || in.Operation == "" || len(in.Operation) > 64 ||
		!responseHash(in.KeyHash) || !responseHash(in.NamespaceHash) || !responseHash(in.RequestHash) || !responseHash(in.DigestVersion) || len(in.Model) == 0 || len(in.Model) > 256 ||
		in.Stage != string(bg.StageQueued) || in.Revision != 1 || in.CreatedAt <= 0 || in.CreatedAt > now || in.Deadline <= now || in.Deadline-in.CreatedAt > int64(5*time.Minute/time.Millisecond) ||
		in.ContentExpiresAt < in.Deadline || in.ContentExpiresAt-in.CreatedAt > int64(time.Hour/time.Millisecond) || in.ExpiresAt < in.ContentExpiresAt || in.ExpiresAt-in.CreatedAt > int64(7*24*time.Hour/time.Millisecond) ||
		len(in.Ciphertext) < 30 || len(in.Ciphertext) > bs.MaxInput+256 || len(in.Snapshot) != 0 || in.ReservedBytes < int64(len(in.Ciphertext)) || in.ReservedBytes > bs.MaxInput+bs.MaxContent+512 ||
		in.ClaimEpoch != 0 || in.ClaimHash != "" || in.ClaimUntil != 0 || in.CancelRequested || in.Deleted || in.ReplayPending || in.DueAt != 0 || in.LastSequence != -1 || in.StoredBytes != 0 || in.EventCount != 0 || !validBackgroundLimits(limits) {
		return out, bg.ErrConflict
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backgroundLock(tx); err != nil {
			return err
		}
		var setting model.Setting
		if err := tx.Where("key = ? AND value = ?", backgroundDigestSetting, in.DigestVersion).First(&setting).Error; err != nil {
			return bg.ErrUnavailable
		}
		binding := identity.Binding{UserID: in.UserID, TokenID: in.TokenID}
		if err := backgroundAuthority(tx, binding, in.Model); err != nil {
			return err
		}
		var existing model.BackgroundJob
		q := tx.Where("user_id = ? AND token_id = ? AND key_hash = ?", in.UserID, in.TokenID, in.KeyHash).Limit(1).Find(&existing)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 0 {
			if existing.RequestHash != in.RequestHash || existing.Operation != in.Operation || existing.Model != in.Model {
				return bg.ErrConflict
			}
			if existing.Deleted || existing.ContentExpiresAt <= now && existing.Stage == string(bg.StageTerminal) {
				return bg.ErrCursorUnavailable
			}
			if existing.NamespaceHash == "" {
				existing.NamespaceHash = in.NamespaceHash
				if e := saveBackground(tx, &existing); e != nil {
					return e
				}
			}
			out = backgroundSnapshot(existing)
			return nil
		}
		if err := backgroundResourceFree(tx, in.PublicID); err != nil {
			return err
		}
		var foreground int64
		if e := tx.Model(&model.ResponseRequest{}).Where("user_id = ? AND token_id = ? AND operation = ? AND key_hash = ? AND (expires_at > ? OR state NOT IN ?)", in.UserID, in.TokenID, in.Operation, in.NamespaceHash, now, []string{model.ResponseCompleted, model.ResponseFailed, model.ResponseRejected}).Count(&foreground).Error; e != nil {
			return e
		}
		if foreground != 0 {
			return bg.ErrConflict
		}
		for _, owner := range []bool{false, true} {
			q := tx.Model(&model.BackgroundJob{})
			maxCount, maxBytes, maxActive := limits.InstanceRecords, limits.InstanceBytes, limits.InstanceActive
			if owner {
				q = q.Where("user_id = ?", in.UserID)
				maxCount, maxBytes, maxActive = limits.OwnerRecords, limits.OwnerBytes, limits.OwnerActive
			}
			var usage struct{ Count, Bytes, Active int64 }
			if err := q.Select("COUNT(*) AS count, COALESCE(SUM(reserved_bytes),0) AS bytes, COALESCE(SUM(CASE WHEN stage <> 'terminal' THEN 1 ELSE 0 END),0) AS active").Scan(&usage).Error; err != nil {
				return err
			}
			if usage.Count >= maxCount || usage.Active >= maxActive || usage.Bytes > maxBytes-in.ReservedBytes {
				return bg.ErrCapacity
			}
		}
		row := backgroundRow(in)
		if row.Snapshot == nil {
			row.Snapshot = []byte{}
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		out = backgroundSnapshot(row)
		return nil
	})
	if err != nil {
		out = bs.Record{}
	}
	return
}

func readBackground(tx *gorm.DB, binding identity.Binding, id string) (row model.BackgroundJob, err error) {
	if !validBackgroundID(id) || binding.UserID == 0 || binding.TokenID == 0 {
		return row, bg.ErrNotFound
	}
	query := tx.Where("public_id = ? AND user_id = ? AND token_id = ? AND length(ciphertext) <= ? AND length(snapshot) <= ?", id, binding.UserID, binding.TokenID, bs.MaxInput+256, bs.MaxContent+256).Limit(1).Find(&row)
	err = query.Error
	if err == nil && query.RowsAffected == 0 {
		return row, bg.ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if row.Deleted || row.ContentExpiresAt <= time.Now().UnixMilli() {
		return row, bg.ErrNotFound
	}
	err = backgroundAuthority(tx, binding, row.Model)
	return
}
func (s *Store) ReadBackground(ctx context.Context, binding identity.Binding, id string) (out bs.Record, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, e := readBackground(tx, binding, id)
		if e == nil {
			out = backgroundSnapshot(row)
		}
		return e
	})
	return
}
func (s *Store) CancelBackground(ctx context.Context, binding identity.Binding, id string) (out bs.Record, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backgroundLock(tx); err != nil {
			return err
		}
		row, e := readBackground(tx, binding, id)
		if e != nil {
			return e
		}
		if row.Stage == string(bg.StageTerminal) || row.CancelRequested {
			out = backgroundSnapshot(row)
			return nil
		}
		if row.Revision >= math.MaxInt64 {
			return bg.ErrConflict
		}
		row.CancelRequested = true
		row.Revision++
		if row.Stage == string(bg.StageQueued) {
			row.Stage = string(bg.StageTerminal)
			row.Ciphertext = []byte{}
			row.ReservedBytes = 0
			row.ClaimHash = ""
			row.ClaimUntil = 0
		}
		row.DueAt = 0
		if e = saveBackground(tx, &row); e != nil {
			return e
		}
		out = backgroundSnapshot(row)
		return nil
	})
	return
}

func (s *Store) ReadBackgroundEvents(ctx context.Context, binding identity.Binding, id string, after int64, limit int) (out []bs.Event, err error) {
	if after < -1 || limit < 1 || limit > 128 {
		return nil, bg.ErrCursorUnavailable
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, e := readBackground(tx, binding, id)
		if e != nil {
			return e
		}
		if !row.Stream || row.ContentExpiresAt <= time.Now().UnixMilli() {
			return bg.ErrCursorUnavailable
		}
		var events []model.BackgroundEvent
		if e = tx.Where("task_id = ? AND sequence > ? AND length(ciphertext) <= ?", row.ID, after, (64<<10)+256).Order("sequence").Limit(limit).Find(&events).Error; e != nil {
			return e
		}
		for _, event := range events {
			out = append(out, bs.Event{TaskID: event.TaskID, Sequence: event.Sequence, Ciphertext: bytes.Clone(event.Ciphertext)})
		}
		return nil
	})
	return
}

func backgroundToken() (string, error) {
	var raw [32]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(raw[:]), nil
}
func backgroundHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func claimDuration(d time.Duration) bool { return d >= time.Second && d <= time.Minute }
func (s *Store) ClaimBackground(ctx context.Context, lease time.Duration) (out bs.Record, claim bs.Claim, err error) {
	if !claimDuration(lease) {
		return out, claim, bg.ErrConflict
	}
	token, e := backgroundToken()
	if e != nil {
		return out, claim, e
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		now := time.Now().UnixMilli()
		// An expired dispatch lease cannot prove that POST was never accepted.
		if e := tx.Model(&model.BackgroundJob{}).Where("stage = ? AND claim_until <= ?", bg.StageDispatching, now).Updates(map[string]interface{}{"stage": bg.StageUncertain, "claim_hash": "", "claim_until": 0, "revision": gorm.Expr("revision + 1")}).Error; e != nil {
			return e
		}
		var row model.BackgroundJob
		query := tx.Where("(stage IN ? OR (stage = ? AND replay_pending = 1)) AND deleted = 0 AND claim_until <= ? AND due_at <= ? AND content_expires_at > ?", []string{string(bg.StageQueued), string(bg.StageRemoteActive)}, bg.StageTerminal, now, now, now).Order("due_at,created_at,id").Limit(1).Find(&row)
		e := query.Error
		if e == nil && query.RowsAffected == 0 {
			return nil
		}
		if e != nil {
			return e
		}
		if row.ClaimEpoch == math.MaxInt64 {
			return bs.ErrClaimLost
		}
		row.ClaimEpoch++
		row.ClaimHash = backgroundHash(token)
		row.ClaimUntil = now + lease.Milliseconds()
		if e = saveBackground(tx, &row); e != nil {
			return e
		}
		out = backgroundSnapshot(row)
		claim = bs.Claim{ID: row.ID, Epoch: row.ClaimEpoch, Token: token}
		return nil
	})
	if err == nil && claim.ID == "" {
		err = bg.ErrNotFound
	}
	if err != nil {
		out, claim = bs.Record{}, bs.Claim{}
	}
	return
}
func ownedBackground(tx *gorm.DB, c bs.Claim) (row model.BackgroundJob, err error) {
	if !responseHash(c.ID) || !responseHash(c.Token) || c.Epoch <= 0 {
		return row, bs.ErrClaimLost
	}
	query := tx.Where("id = ? AND claim_epoch = ? AND claim_hash = ? AND claim_until > ?", c.ID, c.Epoch, backgroundHash(c.Token), time.Now().UnixMilli()).Limit(1).Find(&row)
	err = query.Error
	if err == nil && query.RowsAffected == 0 {
		err = bs.ErrClaimLost
	}
	return
}
func (s *Store) RenewBackground(ctx context.Context, c bs.Claim, lease time.Duration) (cancel bool, err error) {
	if !claimDuration(lease) {
		return false, bg.ErrConflict
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		row, e := ownedBackground(tx, c)
		if e != nil {
			return e
		}
		cancel = row.CancelRequested
		return tx.Model(&row).Update("claim_until", time.Now().Add(lease).UnixMilli()).Error
	})
	return
}
func validBackgroundTransition(from string, to bg.Stage) bool {
	switch bg.Stage(from) {
	case bg.StageQueued:
		return to == bg.StageDispatching || to == bg.StageTerminal
	case bg.StageDispatching:
		return to == bg.StageRemoteActive || to == bg.StageTerminal || to == bg.StageUncertain
	case bg.StageRemoteActive:
		return to == bg.StageRemoteActive || to == bg.StageTerminal || to == bg.StageUncertain
	}
	return false
}
func (s *Store) UpdateBackground(ctx context.Context, c bs.Claim, revision uint64, in bs.Update) (out bs.Record, err error) {
	if len(in.Snapshot) > bs.MaxContent+256 || len(in.Events) > 128 {
		return out, bg.ErrCapacity
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		row, e := ownedBackground(tx, c)
		if e != nil {
			return e
		}
		terminalReplay := row.Stage == string(bg.StageTerminal) && row.ReplayPending && in.Stage == bg.StageTerminal
		if row.Deleted || row.Revision != revision || row.Revision >= math.MaxInt64 || !validBackgroundTransition(row.Stage, in.Stage) && !terminalReplay || in.ReplayPending && (in.Stage != bg.StageTerminal || !row.Stream) {
			return bg.ErrConflict
		}
		if row.Stage == string(bg.StageQueued) && in.Stage == bg.StageDispatching {
			if row.CancelRequested || row.Deadline <= time.Now().UnixMilli() {
				return bg.ErrConflict
			}
			if e := backgroundAuthority(tx, identity.Binding{UserID: row.UserID, TokenID: row.TokenID}, row.Model); e != nil {
				return e
			}
		}
		if row.ContentExpiresAt <= time.Now().UnixMilli() {
			return bg.ErrCursorUnavailable
		}
		if in.Stage == bg.StageRemoteActive && len(in.Snapshot) < 30 {
			return bg.ErrConflict
		}
		if in.ReplayPending && len(in.Snapshot) < 30 {
			return bg.ErrConflict
		}
		for _, event := range in.Events {
			if !row.Stream || event.TaskID != row.ID || event.Sequence < 0 || len(event.Ciphertext) < 30 || len(event.Ciphertext) > (64<<10)+256 {
				return bg.ErrConflict
			}
			var old model.BackgroundEvent
			q := tx.Where("task_id = ? AND sequence = ?", row.ID, event.Sequence).Limit(1).Find(&old)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 0 {
				if !bytes.Equal(old.Ciphertext, event.Ciphertext) {
					return bg.ErrConflict
				}
				continue
			}
			if event.Sequence != row.LastSequence+1 || row.EventCount >= bs.MaxEvents {
				return bg.ErrCursorUnavailable
			}
			if e := tx.Create(&model.BackgroundEvent{TaskID: row.ID, Sequence: event.Sequence, Ciphertext: bytes.Clone(event.Ciphertext)}).Error; e != nil {
				return e
			}
			row.LastSequence = event.Sequence
			row.EventCount++
			row.StoredBytes += int64(len(event.Ciphertext))
		}
		if in.Snapshot != nil {
			row.Snapshot = bytes.Clone(in.Snapshot)
		}
		if row.StoredBytes+int64(len(row.Snapshot))+int64(len(row.Ciphertext)) > row.ReservedBytes {
			return bg.ErrCapacity
		}
		// Leave room for bounded control/error facts after event capacity is hit.
		if len(in.Events) > 0 && row.StoredBytes+int64(len(row.Snapshot))+int64(len(row.Ciphertext)) > row.ReservedBytes-1024 {
			return bg.ErrCapacity
		}
		row.Stage = string(in.Stage)
		row.ReplayPending = in.ReplayPending
		row.Revision++
		row.DueAt = in.DueAt
		if in.Stage == bg.StageTerminal && !in.ReplayPending || in.Stage == bg.StageUncertain {
			row.ClaimHash = ""
			row.ClaimUntil = 0
		}
		if e = saveBackground(tx, &row); e != nil {
			return e
		}
		out = backgroundSnapshot(row)
		return nil
	})
	return
}
func (s *Store) ReleaseBackground(ctx context.Context, c bs.Claim, due int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		row, e := ownedBackground(tx, c)
		if e != nil {
			return e
		}
		if row.Stage == string(bg.StageDispatching) {
			row.Stage = string(bg.StageUncertain)
			row.Revision++
		}
		row.ClaimHash = ""
		row.ClaimUntil = 0
		row.DueAt = due
		return saveBackground(tx, &row)
	})
}
func (s *Store) CleanupBackground(ctx context.Context, limit int) error {
	if limit < 1 || limit > 256 {
		return bg.ErrCapacity
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		var rows []model.BackgroundJob
		now := time.Now().UnixMilli()
		if e := tx.Where("content_expires_at <= ? AND (length(ciphertext) > 0 OR length(snapshot) > 0 OR reserved_bytes > 0)", now).Order("id").Limit(limit).Find(&rows).Error; e != nil {
			return e
		}
		for _, row := range rows {
			if e := tx.Where("task_id = ?", row.ID).Delete(&model.BackgroundEvent{}).Error; e != nil {
				return e
			}
			changes := map[string]interface{}{"ciphertext": []byte{}, "snapshot": []byte{}, "reserved_bytes": 0, "stored_bytes": 0, "replay_pending": false, "revision": gorm.Expr("revision + 1"), "claim_hash": "", "claim_until": 0}
			if row.Stage != string(bg.StageTerminal) {
				changes["stage"] = bg.StageUncertain
			}
			if e := tx.Model(&row).Updates(changes).Error; e != nil {
				return e
			}
		}
		// Only known terminal metadata can expire; uncertainty remains protected.
		var ids []string
		if e := tx.Model(&model.BackgroundJob{}).Where("stage = ? AND expires_at <= ? AND reserved_bytes = 0", bg.StageTerminal, now).Order("id").Limit(limit).Pluck("id", &ids).Error; e != nil {
			return e
		}
		if len(ids) == 0 {
			return nil
		}
		if e := tx.Where("task_id IN ?", ids).Delete(&model.BackgroundEvent{}).Error; e != nil {
			return e
		}
		return tx.Where("id IN ?", ids).Delete(&model.BackgroundJob{}).Error
	})
}

// CleanupExpiredBackground enforces fixed retention before loading new runtime
// keys or serving requests, including when the product feature is disabled.
func (s *Store) CleanupExpiredBackground(ctx context.Context) error {
	for range 21 {
		var count int64
		now := time.Now().UnixMilli()
		if e := s.db.WithContext(ctx).Model(&model.BackgroundJob{}).Where("(content_expires_at <= ? AND (length(ciphertext) > 0 OR length(snapshot) > 0 OR reserved_bytes > 0)) OR (stage = ? AND expires_at <= ? AND reserved_bytes = 0)", now, bg.StageTerminal, now).Count(&count).Error; e != nil {
			return e
		}
		if count == 0 {
			return nil
		}
		if e := s.CleanupBackground(ctx, 200); e != nil {
			return e
		}
	}
	return bg.ErrCapacity
}

var _ bs.Repository = (*Store)(nil)

func saveBackground(tx *gorm.DB, row *model.BackgroundJob) error {
	if row.Ciphertext == nil {
		row.Ciphertext = []byte{}
	}
	if row.Snapshot == nil {
		row.Snapshot = []byte{}
	}
	return tx.Save(row).Error
}
