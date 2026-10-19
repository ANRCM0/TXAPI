package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"minigateway/internal/model"
)

type taskControl struct{ store *Store }

func (s *Store) TaskControl() task.Repository { return &taskControl{s} }

const taskColumns = "id,public_id,user_id,token_id,model,stage,revision,created_at,deadline,content_expires_at,expires_at,cancel_requested,deleted,replay_pending,event_count,last_sequence"

var (
	errSessionUnauthorized = errors.New("current login session required")
	errSessionForbidden    = errors.New("current administrator required")
)

// currentSessionScope is shared by session-authorized metadata repositories.
func currentSessionScope(tx *gorm.DB, session string, administrator bool) (model.User, error) {
	var user model.User
	if !strings.HasPrefix(session, "session-") || len(session) > 256 {
		return user, errSessionUnauthorized
	}
	// Table/Scan does not apply the User model's soft-delete scope.
	result := tx.Table("users").Select("users.id,users.role,users.status").
		Joins("JOIN sessions ON sessions.user_id = users.id").
		Where("sessions.hash = ? AND sessions.expires_at > ? AND users.status = ? AND users.deleted_at IS NULL", access.Hash(session), time.Now().Unix(), model.UserStatusActive).
		Limit(1).Scan(&user)
	if result.Error != nil {
		return user, result.Error
	}
	if result.RowsAffected != 1 || user.ID == 0 {
		return user, errSessionUnauthorized
	}
	if administrator && user.Role != model.RoleAdmin {
		return user, errSessionForbidden
	}
	return user, nil
}

func taskSession(tx *gorm.DB, a task.Access) (model.User, error) {
	user, err := currentSessionScope(tx, a.Session, a.Administrator)
	if errors.Is(err, errSessionUnauthorized) {
		return user, task.ErrUnauthorized
	}
	if errors.Is(err, errSessionForbidden) {
		return user, task.ErrForbidden
	}
	return user, err
}

func taskProjection(row model.BackgroundJob, admin bool) task.Task {
	out := task.Task{ID: row.PublicID, UserID: row.UserID, TokenID: row.TokenID, Model: row.Model, Stage: row.Stage, Revision: row.Revision, CreatedAt: row.CreatedAt, Deadline: row.Deadline, ContentExpiresAt: row.ContentExpiresAt, MetadataExpiresAt: row.ExpiresAt, CancelRequested: row.CancelRequested, Deleted: row.Deleted, ReplayPending: row.ReplayPending, EventCount: row.EventCount, ContentAvailable: !row.Deleted && row.ContentExpiresAt > time.Now().UnixMilli()}
	if row.LastSequence >= 0 {
		n := row.LastSequence
		out.LastSequence = &n
	}
	out.CanCancel = !admin && !row.Deleted && !row.CancelRequested && row.Stage != string(bg.StageTerminal)
	return out
}

func taskQueryValid(a task.Access, q *task.Query) bool {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 10000 || !a.Administrator && q.OwnerID != 0 {
		return false
	}
	switch bg.Stage(q.Stage) {
	case "", bg.StageQueued, bg.StageDispatching, bg.StageRemoteActive, bg.StageTerminal, bg.StageUncertain:
		return true
	}
	return false
}

func (r *taskControl) List(ctx context.Context, a task.Access, q task.Query) (out task.Page, err error) {
	if ctx == nil || !taskQueryValid(a, &q) {
		return out, task.ErrInvalid
	}
	err = r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, e := taskSession(tx, a)
		if e != nil {
			return e
		}
		query := tx.Model(&model.BackgroundJob{}).Select(taskColumns)
		if !a.Administrator {
			query = query.Where("user_id = ?", user.ID)
		} else if q.OwnerID != 0 {
			query = query.Where("user_id = ?", q.OwnerID)
		}
		if q.TokenID != 0 {
			query = query.Where("token_id = ?", q.TokenID)
		}
		if q.Stage != "" {
			query = query.Where("stage = ?", q.Stage)
		}
		var rows []model.BackgroundJob
		if e = query.Order("created_at DESC,public_id DESC").Limit(q.Limit + 1).Offset(q.Offset).Find(&rows).Error; e != nil {
			return e
		}
		out = task.Page{Data: make([]task.Task, 0, min(len(rows), q.Limit)), HasMore: len(rows) > q.Limit}
		for _, row := range rows[:min(len(rows), q.Limit)] {
			out.Data = append(out.Data, taskProjection(row, a.Administrator))
		}
		return nil
	})
	if err != nil {
		out = task.Page{}
	}
	return
}

func taskRow(tx *gorm.DB, a task.Access, owner uint, id string) (row model.BackgroundJob, err error) {
	if !responseIdentifier(id, 80) || !strings.HasPrefix(id, "resp_") {
		return row, task.ErrNotFound
	}
	query := tx.Select(taskColumns).Where("public_id = ?", id)
	if !a.Administrator {
		query = query.Where("user_id = ?", owner)
	}
	result := query.Limit(1).Find(&row)
	if result.Error != nil {
		return row, result.Error
	}
	if result.RowsAffected != 1 {
		return row, task.ErrNotFound
	}
	return row, nil
}

func (r *taskControl) Read(ctx context.Context, a task.Access, id string) (out task.Task, err error) {
	if ctx == nil {
		return out, task.ErrInvalid
	}
	err = r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, e := taskSession(tx, a)
		if e != nil {
			return e
		}
		row, e := taskRow(tx, a, user.ID, id)
		if e != nil {
			return e
		}
		out = taskProjection(row, a.Administrator)
		return nil
	})
	if err != nil {
		out = task.Task{}
	}
	return
}

func (r *taskControl) Cancel(ctx context.Context, a task.Access, id string, revision uint64) (out task.Task, err error) {
	if a.Administrator {
		return out, task.ErrForbidden
	}
	if ctx == nil || revision == 0 || revision > math.MaxInt64 {
		return out, task.ErrInvalid
	}
	err = r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := backgroundLock(tx); e != nil {
			return e
		}
		user, e := taskSession(tx, a)
		if e != nil {
			return e
		}
		row, e := taskRow(tx, a, user.ID, id)
		if e != nil {
			return e
		}
		if row.Deleted {
			return task.ErrNotFound
		}
		if row.Revision != revision {
			return task.ErrConflict
		}
		if e = cancelBackgroundRow(tx, &row); e != nil {
			if errors.Is(e, bg.ErrConflict) {
				return task.ErrConflict
			}
			return e
		}
		out = taskProjection(row, false)
		return nil
	})
	if err != nil {
		out = task.Task{}
	}
	return
}

var _ task.Repository = (*taskControl)(nil)
