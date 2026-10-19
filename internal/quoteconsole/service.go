// Package quoteconsole authorizes read-only administrator selling quotations.
package quoteconsole

import (
	"context"
	"strconv"
	"time"

	admission "minigateway/internal/foundation/contracts/admission/v1"
	control "minigateway/internal/foundation/contracts/control/v1"
	q "minigateway/internal/foundation/contracts/quotation/v1"
)

type Sessions interface {
	SessionUser(string) (*control.User, error)
}

type service struct {
	reader   q.Reader
	sessions Sessions
	limiter  admission.Limiter
}

func New(reader q.Reader, sessions Sessions, limiter admission.Limiter) q.Service {
	return &service{reader: reader, sessions: sessions, limiter: limiter}
}

func (s *service) authorize(ctx context.Context, access q.Access) error {
	if ctx == nil || s.reader == nil || s.sessions == nil || s.limiter == nil {
		return q.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	user, err := s.sessions.SessionUser(access.Session)
	if err != nil || user == nil || user.ID == 0 || user.Status != "active" {
		return q.ErrUnauthorized
	}
	if user.Role != control.RoleAdmin {
		return q.ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bucket := "quotation-console:user:" + strconv.FormatUint(uint64(user.ID), 10)
	allowed, _ := s.limiter.AllowRequest(ctx, bucket, 120, time.Now())
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowed {
		return q.ErrRateLimited
	}
	return nil
}

func (s *service) Current(ctx context.Context, access q.Access) (q.Current, error) {
	if err := s.authorize(ctx, access); err != nil {
		return q.Current{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.reader.Current(ctx)
}

func (s *service) Calculate(ctx context.Context, access q.Access, input q.Input) (q.Quote, error) {
	if err := s.authorize(ctx, access); err != nil {
		return q.Quote{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.reader.Calculate(ctx, input)
}

var _ q.Service = (*service)(nil)
