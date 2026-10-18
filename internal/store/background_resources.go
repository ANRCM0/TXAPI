package store

import (
	"context"
	"math"
	"time"

	"gorm.io/gorm"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func (s *Store) BackgroundKind(ctx context.Context, b identity.Binding, id string) (kind bg.ResourceKind, err error) {
	if !responseIdentifier(id, 80) || len(id) < 6 || id[:5] != "resp_" || b.UserID == 0 || b.TokenID == 0 {
		return "", bg.ErrNotFound
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct{ Kind, Model string }
		now := time.Now().UnixMilli()
		// One authority index, not a not-found fallback between content owners.
		e := tx.Raw(`SELECT 'background' AS kind, model FROM background_jobs
			WHERE public_id = ? AND user_id = ? AND token_id = ? AND deleted = 0 AND content_expires_at > ?
			UNION SELECT 'foreground' AS kind, model FROM response_histories
			WHERE id = ? AND user_id = ? AND token_id = ? AND kind = 'response' AND deleted = 0 AND expires_at > ?
			UNION SELECT 'foreground' AS kind, model FROM response_requests
			WHERE response_id = ? AND user_id = ? AND token_id = ? AND expires_at > ?
			AND NOT EXISTS (SELECT 1 FROM response_histories h WHERE h.id = response_requests.response_id AND h.deleted = 1)`,
			id, b.UserID, b.TokenID, now, id, b.UserID, b.TokenID, now, id, b.UserID, b.TokenID, now).Scan(&rows).Error
		if e != nil {
			return e
		}
		if len(rows) != 1 {
			return bg.ErrNotFound
		}
		if e = backgroundAuthority(tx, b, rows[0].Model); e != nil {
			return e
		}
		kind = bg.ResourceKind(rows[0].Kind)
		return nil
	})
	if err != nil {
		kind = ""
	}
	return
}

func (s *Store) DeleteBackground(ctx context.Context, b identity.Binding, id string, revision *uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		row, e := readBackground(tx, b, id)
		if e != nil {
			return e
		}
		if row.Stage != string(bg.StageTerminal) || row.Revision >= math.MaxInt64 || revision != nil && row.Revision != *revision {
			return bg.ErrConflict
		}
		if e = tx.Where("task_id = ?", row.ID).Delete(&model.BackgroundEvent{}).Error; e != nil {
			return e
		}
		row.Deleted = true
		row.ReplayPending = false
		row.Ciphertext, row.Snapshot = []byte{}, []byte{}
		row.ReservedBytes, row.StoredBytes, row.EventCount = 0, 0, 0
		row.ClaimHash, row.ClaimUntil = "", 0
		row.Revision++
		return saveBackground(tx, &row)
	})
}

func (s *Store) ReadClaimedBackground(ctx context.Context, c bs.Claim) (out bs.Record, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, e := ownedBackground(tx, c)
		if e == nil {
			out = backgroundSnapshot(row)
		}
		return e
	})
	return
}

// ReadClaimedBackgroundEvent exposes only one bounded ciphertext to its fenced
// owner. AEAD and semantic replay comparison remain the generation owner's job.
func (s *Store) ReadClaimedBackgroundEvent(ctx context.Context, c bs.Claim, sequence int64) (out bs.Event, found bool, err error) {
	if sequence < 0 {
		return out, false, bg.ErrCursorUnavailable
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, e := ownedBackground(tx, c)
		if e != nil {
			return e
		}
		if row.Deleted || row.ContentExpiresAt <= time.Now().UnixMilli() {
			return bs.ErrClaimLost
		}
		var event model.BackgroundEvent
		q := tx.Where("task_id = ? AND sequence = ? AND length(ciphertext) <= ?", row.ID, sequence, (64<<10)+256).Limit(1).Find(&event)
		if q.Error != nil {
			return q.Error
		}
		found = q.RowsAffected != 0
		if found {
			out = bs.Event{TaskID: event.TaskID, Sequence: event.Sequence, Ciphertext: event.Ciphertext}
		}
		return nil
	})
	return
}

func (s *Store) RequestBackgroundCancel(ctx context.Context, c bs.Claim) (out bs.Record, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		row, e := ownedBackground(tx, c)
		if e != nil {
			return e
		}
		if row.Stage != string(bg.StageRemoteActive) || row.Revision >= math.MaxInt64 {
			return bg.ErrConflict
		}
		if !row.CancelRequested {
			row.CancelRequested = true
			row.Revision++
			if e = saveBackground(tx, &row); e != nil {
				return e
			}
		}
		out = backgroundSnapshot(row)
		return nil
	})
	return
}
