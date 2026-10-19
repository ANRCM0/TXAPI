// Package ownerbilling authorizes current-owner financial reads.
package ownerbilling

import (
	"context"
	"strconv"
	"time"

	admission "minigateway/internal/foundation/contracts/admission/v1"
	control "minigateway/internal/foundation/contracts/control/v1"
	ledger "minigateway/internal/foundation/contracts/ownerledger/v1"
)

type Sessions interface {
	SessionUser(string) (*control.User, error)
}

type service struct {
	repository ledger.Repository
	sessions   Sessions
	limiter    admission.Limiter
}

func New(repository ledger.Repository, sessions Sessions, limiter admission.Limiter) ledger.Service {
	return &service{repository: repository, sessions: sessions, limiter: limiter}
}

func (s *service) authorize(ctx context.Context, access ledger.Access) error {
	if ctx == nil || s.repository == nil || s.sessions == nil || s.limiter == nil {
		return ledger.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	user, err := s.sessions.SessionUser(access.Session)
	if err != nil || user == nil || user.ID == 0 || user.Status != "active" {
		return ledger.ErrUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := "owner-ledger:user:" + strconv.FormatUint(uint64(user.ID), 10)
	allowed, _ := s.limiter.AllowRequest(ctx, key, 120, time.Now())
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowed {
		return ledger.ErrRateLimited
	}
	return nil
}

func (s *service) Summary(ctx context.Context, access ledger.Access) (ledger.Summary, error) {
	if err := s.authorize(ctx, access); err != nil {
		return ledger.Summary{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repository.Summary(ctx, access)
}

func (s *service) List(ctx context.Context, access ledger.Access, query ledger.Query) (ledger.Page, error) {
	if err := s.authorize(ctx, access); err != nil {
		return ledger.Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repository.List(ctx, access, query)
}

func (s *service) Read(ctx context.Context, access ledger.Access, id string) (ledger.Detail, error) {
	if err := s.authorize(ctx, access); err != nil {
		return ledger.Detail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.repository.Read(ctx, access, id)
}

var _ ledger.Service = (*service)(nil)
