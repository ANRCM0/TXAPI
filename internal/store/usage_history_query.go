package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/model"
)

type usageRepository struct {
	store     *Store
	enabled   bool
	retention int
}

func (s *Store) UsageHistory(enabled bool, retentionSeconds int) history.Repository {
	return &usageRepository{s, enabled, retentionSeconds}
}

func usageSession(tx *gorm.DB, a history.Access) (model.User, error) {
	u, e := currentSessionScope(tx, a.Session, a.Administrator)
	if errors.Is(e, errSessionUnauthorized) {
		return u, history.ErrUnauthorized
	}
	if errors.Is(e, errSessionForbidden) {
		return u, history.ErrForbidden
	}
	return u, e
}

func usageSummary(row model.UsageInvocation, admin bool) history.Summary {
	out := history.Summary{Transport: row.Transport, ID: row.ID, UserID: row.UserID, TokenID: row.TokenID, RequestID: row.RequestID, Operation: execution.Operation(row.Operation), Model: row.Model, Stream: row.Stream, State: row.State, CreatedAt: row.CreatedAt, FinishedAt: row.FinishedAt, ExpiresAt: row.ExpiresAt, AttemptCount: row.AttemptCount, Host: row.Host}
	if admin {
		out.QuotationVersion = row.QuotationVersion
	}
	return out
}

func usageString(n *int64) *string {
	if n == nil {
		return nil
	}
	s := strconv.FormatInt(*n, 10)
	return &s
}

func usageAttemptView(row model.UsageAttemptReceipt, admin bool) history.AttemptView {
	out := history.AttemptView{Number: row.Number, State: row.State, MayDispatch: row.MayDispatch, Host: row.Host, Usage: history.UsageView{InputTokens: usageString(row.InputTokens), OutputTokens: usageString(row.OutputTokens), TotalTokens: usageString(row.TotalTokens), CachedInputTokens: usageString(row.CachedInputTokens), CacheWriteInputTokens: usageString(row.CacheWriteInputTokens), ReasoningOutputTokens: usageString(row.ReasoningOutputTokens), Completeness: row.Completeness, Source: row.Source, Profile: row.Profile}}
	if admin {
		out.ChannelID = row.ChannelID
		out.CredentialID = row.CredentialID
		out.ActualModel = row.ActualModel
		out.Group = row.Group
		out.Protocol = row.Protocol
	}
	return out
}

func (r *usageRepository) List(ctx context.Context, a history.Access, q history.Query) (out history.Page, err error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 10000 || !a.Administrator && q.OwnerID != 0 {
		return out, history.ErrInvalid
	}
	switch q.State {
	case "", "open", "sealed", "interrupted":
	default:
		return out, history.ErrInvalid
	}
	c, cancel, e := usageContext(ctx)
	if e != nil {
		return out, e
	}
	defer cancel()
	err = r.store.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		u, e := usageSession(tx, a)
		if e != nil {
			return e
		}
		query := tx.Model(&model.UsageInvocation{}).Where("expires_at > ? OR state = ?", time.Now().UnixMilli(), "open")
		if !a.Administrator {
			query = query.Where("user_id = ?", u.ID)
		} else if q.OwnerID != 0 {
			query = query.Where("user_id = ?", q.OwnerID)
		}
		if q.TokenID != 0 {
			query = query.Where("token_id = ?", q.TokenID)
		}
		if q.State != "" {
			query = query.Where("state = ?", q.State)
		}
		var rows []model.UsageInvocation
		if e = query.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Offset(q.Offset).Find(&rows).Error; e != nil {
			return e
		}
		out = history.Page{Data: make([]history.Summary, 0, min(len(rows), q.Limit)), HasMore: len(rows) > q.Limit, Enabled: r.enabled, RetentionSeconds: r.retention}
		for _, row := range rows[:min(len(rows), q.Limit)] {
			out.Data = append(out.Data, usageSummary(row, a.Administrator))
		}
		return nil
	})
	if err != nil {
		out = history.Page{}
	}
	return out, usageError(err)
}

func (r *usageRepository) Read(ctx context.Context, a history.Access, id string) (out history.Detail, err error) {
	c, cancel, e := usageContext(ctx)
	if e != nil {
		return out, e
	}
	defer cancel()
	err = r.store.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		u, e := usageSession(tx, a)
		if e != nil {
			return e
		}
		if !usageID(id) {
			return history.ErrNotFound
		}
		query := tx.Where("id = ? AND (expires_at > ? OR state = ?)", id, time.Now().UnixMilli(), "open")
		if !a.Administrator {
			query = query.Where("user_id = ?", u.ID)
		}
		var row model.UsageInvocation
		res := query.Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return history.ErrNotFound
		}
		if row.MaxAttempts < 1 || row.MaxAttempts > 11 || row.AttemptCount < 0 || row.AttemptCount > row.MaxAttempts {
			return history.ErrUnavailable
		}
		var attempts []model.UsageAttemptReceipt
		if e = tx.Where("invocation_id = ?", id).Order("number ASC").Limit(row.MaxAttempts + 1).Find(&attempts).Error; e != nil {
			return e
		}
		if len(attempts) != row.AttemptCount {
			return history.ErrUnavailable
		}
		out = history.Detail{Record: usageSummary(row, a.Administrator), Attempts: make([]history.AttemptView, 0, len(attempts))}
		for _, attempt := range attempts {
			if attempt.Number < 1 || attempt.Number > row.MaxAttempts {
				return history.ErrUnavailable
			}
			out.Attempts = append(out.Attempts, usageAttemptView(attempt, a.Administrator))
		}
		return nil
	})
	if err != nil {
		out = history.Detail{}
	}
	return out, usageError(err)
}

var _ history.Repository = (*usageRepository)(nil)
