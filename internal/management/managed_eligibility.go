package management

import (
	"context"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	eligibility "minigateway/internal/foundation/contracts/upstreamauth/v3"
)

func (s *Configuration) SetEligibilityRepository(repo eligibility.EligibilityAdmin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eligibility = repo
}

func (s *Configuration) ReadEligibility(ctx context.Context, id uint) (eligibility.Eligibility, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eligibility == nil {
		return eligibility.Eligibility{}, managed.ErrUnavailable
	}
	return s.eligibility.ReadEligibility(ctx, id)
}

func (s *Configuration) ResumeEligibility(ctx context.Context, in eligibility.ResumeRequest) (eligibility.Eligibility, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eligibility == nil {
		return eligibility.Eligibility{}, managed.ErrUnavailable
	}
	return s.eligibility.ResumeEligibility(ctx, in)
}

var _ eligibility.EligibilityAdmin = (*Configuration)(nil)
