// Package usageconsole authorizes metadata queries without execution or billing.
package usageconsole

import (
	"context"
	"strconv"
	"time"

	admission "minigateway/internal/foundation/contracts/admission/v1"
	control "minigateway/internal/foundation/contracts/control/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

type Sessions interface {
	SessionUser(string) (*control.User, error)
}

type service struct {
	repository history.Repository
	sessions   Sessions
	limiter    admission.Limiter
}

func New(repository history.Repository, sessions Sessions, limiter admission.Limiter) history.Service {
	return &service{repository: repository, sessions: sessions, limiter: limiter}
}

func (s *service) authorize(ctx context.Context, access history.Access) error {
	if ctx == nil || s.repository == nil || s.sessions == nil || s.limiter == nil {
		return history.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	user, err := s.sessions.SessionUser(access.Session)
	if err != nil || user == nil || user.ID == 0 || user.Status != "active" {
		return history.ErrUnauthorized
	}
	if access.Administrator && user.Role != control.RoleAdmin {
		return history.ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := "usage-history:user:" + strconv.FormatUint(uint64(user.ID), 10)
	allowed, _ := s.limiter.AllowRequest(ctx, key, 120, time.Now())
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowed {
		return history.ErrRateLimited
	}
	return nil
}

func (s *service) List(ctx context.Context, access history.Access, query history.Query) (history.Page, error) {
	if err := s.authorize(ctx, access); err != nil {
		return history.Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repository.List(ctx, access, query)
}

func (s *service) Read(ctx context.Context, access history.Access, id string) (history.Detail, error) {
	if err := s.authorize(ctx, access); err != nil {
		return history.Detail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repository.Read(ctx, access, id)
}

var _ history.Service = (*service)(nil)
