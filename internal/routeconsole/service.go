// Package routeconsole owns session-authorized routing observation queries.
package routeconsole

import (
	"context"
	"strconv"
	"time"

	admission "minigateway/internal/foundation/contracts/admission/v1"
	control "minigateway/internal/foundation/contracts/control/v1"
	routes "minigateway/internal/foundation/contracts/routes/v1"
)

type Sessions interface {
	SessionUser(string) (*control.User, error)
}
type service struct {
	reader   routes.Reader
	sessions Sessions
	limiter  admission.Limiter
}

func New(reader routes.Reader, sessions Sessions, limiter admission.Limiter) routes.Service {
	return &service{reader: reader, sessions: sessions, limiter: limiter}
}

func (s *service) authorize(ctx context.Context, access routes.Access) (routes.Scope, error) {
	if ctx == nil || s.reader == nil || s.sessions == nil || s.limiter == nil {
		return routes.Scope{}, routes.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return routes.Scope{}, err
	}
	user, err := s.sessions.SessionUser(access.Session)
	if err != nil || user == nil || user.ID == 0 || user.Status != "active" {
		return routes.Scope{}, routes.ErrUnauthorized
	}
	if access.Administrator && user.Role != control.RoleAdmin {
		return routes.Scope{}, routes.ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return routes.Scope{}, err
	}
	bucket := "routing-console:user:" + strconv.FormatUint(uint64(user.ID), 10)
	if ok, _ := s.limiter.AllowRequest(ctx, bucket, 120, time.Now()); !ok {
		return routes.Scope{}, routes.ErrRateLimited
	}
	return routes.Scope{UserID: user.ID, Administrator: access.Administrator}, nil
}

func (s *service) List(ctx context.Context, access routes.Access, query routes.Query) (routes.Page, error) {
	scope, err := s.authorize(ctx, access)
	if err != nil {
		return routes.Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.reader.List(ctx, scope, query)
}

func (s *service) Read(ctx context.Context, access routes.Access, id string) (routes.Trace, error) {
	scope, err := s.authorize(ctx, access)
	if err != nil {
		return routes.Trace{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.reader.Read(ctx, scope, id)
}
