// Package controlplane owns management application rules independently of HTTP.
package controlplane

import (
	"errors"
	control "minigateway/internal/foundation/contracts/control/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"sync"
	"time"
)

type loginBucket struct {
	count int
	until time.Time
}
type loginGate struct {
	mu      sync.Mutex
	buckets map[string]loginBucket
	sweep   time.Time
}

func (g *loginGate) allow(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.buckets == nil {
		g.buckets = map[string]loginBucket{}
	}
	if now.After(g.sweep) {
		for k, b := range g.buckets {
			if now.After(b.until) {
				delete(g.buckets, k)
			}
		}
		g.sweep = now.Add(time.Minute)
	}
	b, exists := g.buckets[ip]
	if !exists && len(g.buckets) >= 4096 {
		return false
	}
	if now.After(b.until) {
		b = loginBucket{until: now.Add(time.Minute)}
	}
	if b.count >= 5 {
		return false
	}
	b.count++
	g.buckets[ip] = b
	return true
}

type Service struct {
	repo control.Repository
	gate loginGate
}

func New(repo control.Repository) *Service       { return &Service{repo: repo} }
func (s *Service) AllowLogin(client string) bool { return s.gate.allow(client) }
func (s *Service) Login(name, password string) (string, *control.User, error) {
	if len(name) > 64 || len(password) > 72 {
		return "", nil, errors.New("invalid username or password")
	}
	return s.repo.Login(name, password)
}
func (s *Service) CreateOwnedKey(owner uint, in *control.TokenInput) (*control.Token, error) {
	if in != nil && in.UserID != 0 && in.UserID != owner {
		return nil, control.ErrForbidden
	}
	return s.repo.IssueToken(owner, in)
}
func (s *Service) ModelsForPolicy(userPolicy identity.Policy) (control.ModelListing, error) {
	system, err := s.repo.SystemPolicy()
	if err != nil {
		return control.ModelListing{}, err
	}
	policy := identity.Intersect(system, userPolicy)
	seen := map[string]bool{}
	models := []string{}
	for _, ch := range s.repo.ModelChannels() {
		if !ch.Enabled {
			continue
		}
		candidates := append([]string(nil), ch.Models...)
		for _, name := range ch.Models {
			if name == "*" {
				candidates = append(candidates, policy.Models...)
				break
			}
		}
		for _, name := range candidates {
			if name != "*" && !seen[name] && policy.Allows(name) {
				models = append(models, name)
				seen[name] = true
			}
		}
	}
	return control.ModelListing{Models: models, Policy: policy}, nil
}
func (s *Service) Logout(secret string) error { return s.repo.Logout(secret) }
func (s *Service) SessionUser(secret string) (*control.User, error) {
	return s.repo.SessionUser(secret)
}
func (s *Service) ListUsers() ([]control.User, error)     { return s.repo.ListUsers() }
func (s *Service) GetUser(id uint) (*control.User, error) { return s.repo.GetUser(id) }
func (s *Service) CreateUser(in *control.UserInput) (*control.User, error) {
	return s.repo.CreateUser(in)
}
func (s *Service) UpdateUser(id uint, in *control.UserInput) (*control.User, error) {
	return s.repo.UpdateUser(id, in)
}
func (s *Service) SystemPolicy() (identity.Policy, error)   { return s.repo.SystemPolicy() }
func (s *Service) SetSystemPolicy(in identity.Policy) error { return s.repo.SetSystemPolicy(in) }
func (s *Service) ListTokens() []control.Token              { return s.repo.ListTokens() }
func (s *Service) IssueToken(owner uint, in *control.TokenInput) (*control.Token, error) {
	return s.repo.IssueToken(owner, in)
}
func (s *Service) UpdateToken(id uint, in *control.TokenInput) (*control.Token, error) {
	return s.repo.UpdateToken(id, in)
}
func (s *Service) DeleteToken(id uint) error { return s.repo.DeleteToken(id) }
func (s *Service) SetTokenQuota(id uint, q float64) (*control.Token, error) {
	return s.repo.SetTokenQuota(id, q)
}
func (s *Service) ClaimToken(id, owner uint) (*control.Token, error) {
	return s.repo.ClaimToken(id, owner)
}
func (s *Service) OwnerTokens(owner uint) ([]control.Token, error) { return s.repo.OwnerTokens(owner) }
func (s *Service) OwnedToken(owner, id uint) (*control.Token, error) {
	return s.repo.OwnedToken(owner, id)
}
func (s *Service) UpdateOwnedToken(owner, id uint, in *control.TokenInput) (*control.Token, error) {
	return s.repo.UpdateOwnedToken(owner, id, in)
}
func (s *Service) DeleteOwnedToken(owner, id uint) error { return s.repo.DeleteOwnedToken(owner, id) }
func (s *Service) ListLogs(q control.LogQuery) ([]control.RequestLog, error) {
	return s.repo.ListLogs(q)
}
func (s *Service) UsageForUser(owner uint) (*control.LogSummary, error) {
	return s.repo.UsageForUser(owner)
}
func (s *Service) LogSummary(since time.Time) (*control.LogSummary, error) {
	return s.repo.LogSummary(since)
}
func (s *Service) StatsByModel(since time.Time) ([]control.ModelStat, error) {
	return s.repo.StatsByModel(since)
}
func (s *Service) StatsByChannel(since time.Time) ([]control.ChannelStat, error) {
	return s.repo.StatsByChannel(since)
}
func (s *Service) Abilities() ([]control.Ability, error) { return s.repo.Abilities() }

var _ control.Service = (*Service)(nil)
