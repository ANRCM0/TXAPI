package management

import (
	"context"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
)

// SetManagedRepository is assembly-only and shares the existing configuration
// mutex and publication owner. A nil repository keeps this capability disabled.
func (s *Configuration) SetManagedRepository(repo managed.Repository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managed = repo
}

func (s *Configuration) CreateManagedChannel(ctx context.Context, in *managed.ManagedChannelInput) (*managed.ManagedChannel, cfg.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managed == nil {
		return nil, cfg.Change{}, managed.ErrUnavailable
	}
	result, err := s.managed.CreateManagedChannel(ctx, in)
	return result, finishChange(&s.channels, err), err
}

func (s *Configuration) GetManagedCredential(ctx context.Context, id uint) (*managed.ManagedCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managed == nil {
		return nil, managed.ErrUnavailable
	}
	return s.managed.GetManagedCredential(ctx, id)
}

func (s *Configuration) UpdateManagedCredential(ctx context.Context, id uint, revision int64, in *managed.ManagedBindingInput) (*managed.ManagedCredential, cfg.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managed == nil {
		return nil, cfg.Change{}, managed.ErrUnavailable
	}
	result, err := s.managed.UpdateManagedCredential(ctx, id, revision, in)
	return result, finishChange(&s.channels, err), err
}

func (s *Configuration) DisableManagedCredential(ctx context.Context, id uint, revision int64) (*managed.ManagedCredential, cfg.Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managed == nil {
		return nil, cfg.Change{}, managed.ErrUnavailable
	}
	result, err := s.managed.DisableManagedCredential(ctx, id, revision)
	return result, finishChange(&s.channels, err), err
}

var _ managed.Service = (*Configuration)(nil)
