// Package taskconsole coordinates metadata queries without content authority.
package taskconsole

import (
	"context"
	admission "minigateway/internal/foundation/contracts/admission/v1"
	control "minigateway/internal/foundation/contracts/control/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"strconv"
	"time"
)

type Sessions interface {
	SessionUser(string) (*control.User, error)
}
type service struct {
	repo     task.Repository
	sessions Sessions
	limiter  admission.Limiter
}

func New(repo task.Repository, sessions Sessions, limiter admission.Limiter) task.Service {
	return &service{repo, sessions, limiter}
}
func (s *service) admit(ctx context.Context, a task.Access) error {
	if ctx == nil || s.repo == nil || s.sessions == nil || s.limiter == nil {
		return task.ErrUnavailable
	}
	user, err := s.sessions.SessionUser(a.Session)
	if err != nil || user == nil || user.ID == 0 {
		return task.ErrUnauthorized
	}
	if a.Administrator && user.Role != control.RoleAdmin {
		return task.ErrForbidden
	}
	if ok, _ := s.limiter.AllowRequest(ctx, "task-console:user:"+strconv.FormatUint(uint64(user.ID), 10), 120, time.Now()); !ok {
		return task.ErrRateLimited
	}
	return nil
}
func (s *service) List(ctx context.Context, a task.Access, q task.Query) (task.Page, error) {
	if err := s.admit(ctx, a); err != nil {
		return task.Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repo.List(ctx, a, q)
}
func (s *service) Read(ctx context.Context, a task.Access, id string) (task.Task, error) {
	if err := s.admit(ctx, a); err != nil {
		return task.Task{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repo.Read(ctx, a, id)
}
func (s *service) Cancel(ctx context.Context, a task.Access, id string, revision uint64) (task.Task, error) {
	if a.Administrator {
		return task.Task{}, task.ErrForbidden
	}
	if err := s.admit(ctx, a); err != nil {
		return task.Task{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repo.Cancel(ctx, a, id, revision)
}
