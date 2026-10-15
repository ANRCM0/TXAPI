package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

type TokenInput struct {
	Name string `json:"name"`
	// Key is only accepted by the internal seed/import compatibility method.
	Key              string   `json:"key"`
	UserID           uint     `json:"user_id"`
	ModelMode        string   `json:"model_mode"`
	Models           []string `json:"models"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
	Quota            float64  `json:"quota"`
	RPMLimit         int      `json:"rpm_limit"`
	TPMLimit         int      `json:"tpm_limit"`
	ConcurrencyLimit int      `json:"concurrency_limit"`
	Status           string   `json:"status"`
}

func (in *TokenInput) validate(now time.Time) error {
	if in.ExpiresInSeconds < 0 || in.ExpiresInSeconds > 315360000 {
		return errors.New("expiry must be between 0 and 10 years")
	}
	if in.Quota < 0 || in.RPMLimit < 0 || in.TPMLimit < 0 || in.ConcurrencyLimit < 0 {
		return errors.New("key limits cannot be negative")
	}
	if in.Status != "" && in.Status != model.TokenStatusActive && in.Status != model.TokenStatusDisabled {
		return errors.New("invalid key status")
	}
	return nil
}
func (in *TokenInput) toModel(now time.Time) *model.Token {
	var expires int64
	if in.ExpiresInSeconds > 0 {
		expires = now.Unix() + in.ExpiresInSeconds
	}
	status := in.Status
	if status == "" {
		status = model.TokenStatusActive
	}
	return &model.Token{UserID: in.UserID, Name: in.Name, KeyHash: access.Hash(in.Key), KeyPrefix: keyPrefix(in.Key), ModelMode: in.ModelMode, Models: model.StringList(in.Models), Quota: in.Quota, RPMLimit: in.RPMLimit, TPMLimit: in.TPMLimit, ConcurrencyLimit: in.ConcurrencyLimit, ExpiresAt: expires, Status: status}
}

// CreateToken imports a known key (seed/fixtures). HTTP callers use IssueToken,
// which generates 256 random bits. An unowned import is quarantined by LookupToken.
func (s *Store) CreateToken(in *TokenInput) (*model.Token, error) {
	if in.Key == "" {
		return nil, errors.New("import key cannot be empty")
	}
	if err := in.validate(time.Now()); err != nil {
		return nil, err
	}
	if in.ModelMode == "" {
		p := access.Legacy(in.Models)
		in.ModelMode = p.Mode
		in.Models = p.Models
	}
	if err := (access.Policy{Mode: in.ModelMode, Models: in.Models}).Validate(); err != nil {
		return nil, err
	}
	if in.UserID > 0 {
		if err := s.EnsureActiveOwner(in.UserID); err != nil {
			return nil, err
		}
	}
	token := in.toModel(time.Now())
	var duplicate int64
	if err := s.db.Unscoped().Model(&model.Token{}).Where("key_hash = ?", token.KeyHash).Count(&duplicate).Error; err != nil {
		return nil, err
	}
	if duplicate > 0 {
		return nil, errors.New("key already exists")
	}
	if err := s.db.Create(token).Error; err != nil {
		return nil, errors.New("could not create API key")
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	// This copy is never the cached/persisted row.
	token.Key = in.Key
	return token, nil
}
func (s *Store) UpdateToken(id uint, in *TokenInput) (*model.Token, error) {
	if err := in.validate(time.Now()); err != nil {
		return nil, err
	}
	old, err := s.GetToken(id)
	if err != nil {
		return nil, err
	}
	if in.Key != "" && access.Hash(in.Key) != old.KeyHash {
		return nil, errors.New("cannot change API key material")
	}
	if in.UserID != 0 && in.UserID != old.UserID {
		return nil, errors.New("use the explicit legacy claim operation to assign ownership")
	}
	if in.ModelMode == "" {
		if in.Models == nil {
			in.ModelMode = old.ModelMode
			in.Models = []string(old.Models)
		} else {
			in.ModelMode = access.Allowlist
		}
	}
	if err := (access.Policy{Mode: in.ModelMode, Models: in.Models}).Validate(); err != nil {
		return nil, err
	}
	updated := in.toModel(time.Now())
	changes := map[string]any{"name": in.Name, "model_mode": in.ModelMode, "models": updated.Models, "quota": in.Quota, "rpm_limit": in.RPMLimit, "tpm_limit": in.TPMLimit, "concurrency_limit": in.ConcurrencyLimit, "expires_at": updated.ExpiresAt}
	if in.Status != "" {
		changes["status"] = in.Status
	}
	if err := s.db.Model(&model.Token{}).Where("id = ?", id).Updates(changes).Error; err != nil {
		return nil, err
	}
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s.GetToken(id)
}
func (s *Store) DeleteToken(id uint) error {
	result := s.db.Delete(&model.Token{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return s.reloadCache()
}
func (s *Store) GetToken(id uint) (*model.Token, error) {
	var token model.Token
	if err := s.db.First(&token, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("token %d: %w", id, ErrNotFound)
		}
		return nil, err
	}
	return &token, nil
}
func (s *Store) ListTokens() []model.Token {
	// Never serialize cached plaintext; the legacy column is empty after v2.
	var tokens []model.Token
	if err := s.db.Order("id").Find(&tokens).Error; err != nil {
		return nil
	}
	return tokens
}
func (s *Store) CountTokens() (int64, error) {
	var count int64
	err := s.db.Model(&model.Token{}).Count(&count).Error
	return count, err
}
