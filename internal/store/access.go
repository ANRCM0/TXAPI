package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	"minigateway/internal/model"
)

type UserInput struct {
	Username         string   `json:"username"`
	Password         string   `json:"password"`
	Role             string   `json:"role"`
	Status           string   `json:"status"`
	ModelMode        string   `json:"model_mode"`
	Models           []string `json:"models"`
	Quota            float64  `json:"quota"`
	RPMLimit         int      `json:"rpm_limit"`
	TPMLimit         int      `json:"tpm_limit"`
	ConcurrencyLimit int      `json:"concurrency_limit"`
}

func (in *UserInput) validate() error {
	in.Username = strings.TrimSpace(in.Username)
	if len(in.Username) < 1 || len(in.Username) > 64 {
		return errors.New("username must contain 1 to 64 bytes")
	}
	if in.Role == "" {
		in.Role = model.RoleUser
	}
	if in.Status == "" {
		in.Status = model.UserStatusActive
	}
	if in.ModelMode == "" {
		in.ModelMode = access.Allowlist
	}
	if in.Role != model.RoleUser && in.Role != model.RoleAdmin {
		return errors.New("invalid role")
	}
	if in.Status != model.UserStatusActive && in.Status != model.UserStatusDisabled {
		return errors.New("invalid user status")
	}
	if in.Quota < 0 || in.RPMLimit < 0 || in.TPMLimit < 0 || in.ConcurrencyLimit < 0 {
		return errors.New("user limits cannot be negative")
	}
	return (access.Policy{Mode: in.ModelMode, Models: in.Models}).Validate()
}
func (s *Store) GetUser(id uint) (*model.User, error) {
	var user model.User
	err := s.db.First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &user, err
}
func (s *Store) ListUsers() ([]model.User, error) {
	var users []model.User
	err := s.db.Order("id").Find(&users).Error
	return users, err
}
func userFrom(in *UserInput, hash string) *model.User {
	return &model.User{Username: in.Username, PasswordHash: hash, Role: in.Role, Status: in.Status, ModelMode: in.ModelMode, Models: model.StringList(in.Models), Quota: in.Quota, RPMLimit: in.RPMLimit, TPMLimit: in.TPMLimit, ConcurrencyLimit: in.ConcurrencyLimit}
}
func (s *Store) BootstrapAdmin(username, password string) (*model.User, error) {
	in := UserInput{Username: username, Password: password, Role: model.RoleAdmin, Status: model.UserStatusActive, ModelMode: access.All}
	if err := in.validate(); err != nil {
		return nil, err
	}
	hash, err := access.PasswordHash(password)
	if err != nil {
		return nil, err
	}
	user := userFrom(&in, hash)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Unscoped().Model(&model.User{}).Where("role = ?", model.RoleAdmin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("administrator already initialized")
		}
		return tx.Create(user).Error
	})
	return user, err
}
func (s *Store) CreateUser(in *UserInput) (*model.User, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	hash, err := access.PasswordHash(in.Password)
	if err != nil {
		return nil, err
	}
	user := userFrom(in, hash)
	if err := s.db.Create(user).Error; err != nil {
		return nil, errors.New("could not create user (username may already exist)")
	}
	return user, nil
}
func (s *Store) UpdateUser(id uint, in *UserInput) (*model.User, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	var hash string
	if in.Password != "" {
		var err error
		hash, err = access.PasswordHash(in.Password)
		if err != nil {
			return nil, err
		}
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var old model.User
		if err := tx.First(&old, id).Error; err != nil {
			return ErrNotFound
		}
		if old.Role == model.RoleAdmin && old.Status == model.UserStatusActive && (in.Role != model.RoleAdmin || in.Status != model.UserStatusActive) {
			var count int64
			if err := tx.Model(&model.User{}).Where("role = ? AND status = ?", model.RoleAdmin, model.UserStatusActive).Count(&count).Error; err != nil {
				return err
			}
			if count <= 1 {
				return errors.New("cannot disable or demote the last active administrator")
			}
		}
		update := map[string]any{"username": in.Username, "role": in.Role, "status": in.Status, "model_mode": in.ModelMode, "models": model.StringList(in.Models), "quota": in.Quota, "rpm_limit": in.RPMLimit, "tpm_limit": in.TPMLimit, "concurrency_limit": in.ConcurrencyLimit}
		if hash != "" {
			update["password_hash"] = hash
		}
		if err := tx.Model(&model.User{}).Where("id = ?", id).Updates(update).Error; err != nil {
			return errors.New("could not update user")
		}
		// Revoke existing management sessions for password/role/status changes.
		if hash != "" || in.Status != old.Status || in.Role != old.Role {
			if err := tx.Where("user_id = ?", id).Delete(&model.Session{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

const systemModelsSetting = "access.system_models"

func (s *Store) SystemPolicy() (access.Policy, error) {
	return systemPolicy(s.db)
}

func systemPolicy(db *gorm.DB) (access.Policy, error) {
	// Explicit all is the documented initial system policy, not an empty allowlist.
	var setting model.Setting
	err := db.Where("key = ?", systemModelsSetting).Take(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return access.Policy{Mode: access.All}, nil
	}
	if err != nil {
		return access.Policy{}, err
	}
	var p access.Policy
	if err = json.Unmarshal([]byte(setting.Value), &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}
func (s *Store) SetSystemPolicy(p access.Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.SetSetting(systemModelsSetting, string(raw))
}

var dummyPasswordHash, _ = access.PasswordHash("unused-login-timing-placeholder")

func (s *Store) Login(username, password string) (string, *model.User, error) {
	var user model.User
	err := s.db.Where("username = ?", username).First(&user).Error
	hash := user.PasswordHash
	if err != nil {
		hash = dummyPasswordHash
	}
	match := access.PasswordMatches(hash, password)
	if err != nil || !match || user.Status != model.UserStatusActive {
		return "", nil, errors.New("invalid username or password")
	}
	secret, err := access.Secret("session-")
	if err != nil {
		return "", nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var current model.User
		if err := tx.First(&current, user.ID).Error; err != nil || current.PasswordHash != user.PasswordHash || current.Status != model.UserStatusActive {
			return errors.New("account changed during login")
		}
		// One cleanup per login bounds expired-session buildup; sessions have a 24h TTL.
		if err := tx.Where("expires_at <= ?", time.Now().Unix()).Delete(&model.Session{}).Error; err != nil {
			return err
		}
		return tx.Create(&model.Session{Hash: access.Hash(secret), UserID: user.ID, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}).Error
	})
	if err != nil {
		return "", nil, err
	}
	return secret, &user, nil
}
func (s *Store) SessionUser(secret string) (*model.User, error) {
	if !strings.HasPrefix(secret, "session-") {
		return nil, errors.New("invalid session")
	}
	var session model.Session
	if err := s.db.Where("hash = ? AND expires_at > ?", access.Hash(secret), time.Now().Unix()).First(&session).Error; err != nil {
		return nil, errors.New("invalid session")
	}
	user, err := s.GetUser(session.UserID)
	if err != nil || user.Status != model.UserStatusActive {
		return nil, errors.New("invalid session")
	}
	return user, nil
}
func (s *Store) Logout(secret string) error {
	return s.db.Where("hash = ?", access.Hash(secret)).Delete(&model.Session{}).Error
}
func (s *Store) OwnedToken(owner, id uint) (*model.Token, error) {
	var token model.Token
	if owner == 0 {
		return nil, ErrNotFound
	}
	if err := s.db.Where("id = ? AND user_id = ?", id, owner).First(&token).Error; err != nil {
		return nil, ErrNotFound
	}
	return &token, nil
}
func (s *Store) OwnerTokens(owner uint) ([]model.Token, error) {
	if owner == 0 {
		return nil, ErrNotFound
	}
	var tokens []model.Token
	err := s.db.Where("user_id = ?", owner).Order("id").Find(&tokens).Error
	return tokens, err
}
func (s *Store) IssueToken(owner uint, in *TokenInput) (*model.Token, error) {
	user, err := s.GetUser(owner)
	if err != nil || user.Status != model.UserStatusActive {
		return nil, errors.New("invalid key owner")
	}
	if in.Key != "" {
		return nil, errors.New("API keys must be generated by the server")
	}
	system, err := s.SystemPolicy()
	if err != nil {
		return nil, err
	}
	if in.ModelMode == "" {
		if in.Models != nil {
			in.ModelMode = access.Allowlist
		} else {
			p := access.Intersect(system, user.Policy())
			in.ModelMode = p.Mode
			in.Models = append([]string(nil), p.Models...)
		}
	}
	policy := access.Policy{Mode: in.ModelMode, Models: in.Models}
	if !access.Subset(policy, access.Intersect(system, user.Policy())) {
		return nil, errors.New("key models exceed owner's effective permissions")
	}
	if in.RPMLimit == 0 {
		in.RPMLimit = user.RPMLimit
	}
	if in.TPMLimit == 0 {
		in.TPMLimit = user.TPMLimit
	}
	if in.ConcurrencyLimit == 0 {
		in.ConcurrencyLimit = user.ConcurrencyLimit
	}
	if in.Quota == 0 {
		in.Quota = user.Quota
	}
	if exceeds(in.RPMLimit, user.RPMLimit) || exceeds(in.TPMLimit, user.TPMLimit) || exceeds(in.ConcurrencyLimit, user.ConcurrencyLimit) {
		return nil, errors.New("key rate limits exceed owner limits")
	}
	if user.Quota > 0 && (in.Quota == 0 || in.Quota > user.Quota) {
		return nil, errors.New("key quota exceeds owner quota")
	}
	key, err := access.Secret("sk-txapi-")
	if err != nil {
		return nil, err
	}
	copy := *in
	copy.Key = key
	copy.UserID = owner
	return s.CreateToken(&copy)
}
func exceeds(child, parent int) bool { return parent > 0 && (child == 0 || child > parent) }
func (s *Store) UpdateOwnedToken(owner, id uint, in *TokenInput) (*model.Token, error) {
	token, err := s.OwnedToken(owner, id)
	if err != nil {
		return nil, err
	}
	if in.UserID != 0 && in.UserID != owner {
		return nil, errors.New("cannot transfer a key")
	}
	if in.Key != "" {
		return nil, errors.New("cannot update key material")
	}
	user, err := s.GetUser(owner)
	if err != nil {
		return nil, err
	}
	system, err := s.SystemPolicy()
	if err != nil {
		return nil, err
	}
	if in.ModelMode == "" {
		if in.Models != nil {
			in.ModelMode = access.Allowlist
		} else {
			in.ModelMode = token.ModelMode
			in.Models = append([]string(nil), token.Models...)
		}
	}
	if !access.Subset(access.Policy{Mode: in.ModelMode, Models: in.Models}, access.Intersect(system, user.Policy())) {
		return nil, errors.New("key models exceed owner permissions")
	}
	if in.RPMLimit == 0 {
		in.RPMLimit = user.RPMLimit
	}
	if in.TPMLimit == 0 {
		in.TPMLimit = user.TPMLimit
	}
	if in.ConcurrencyLimit == 0 {
		in.ConcurrencyLimit = user.ConcurrencyLimit
	}
	if in.Quota == 0 {
		in.Quota = user.Quota
	}
	if exceeds(in.RPMLimit, user.RPMLimit) || exceeds(in.TPMLimit, user.TPMLimit) || exceeds(in.ConcurrencyLimit, user.ConcurrencyLimit) {
		return nil, errors.New("key limits exceed owner limits")
	}
	if user.Quota > 0 && (in.Quota == 0 || in.Quota > user.Quota) {
		return nil, errors.New("key quota exceeds owner quota")
	}
	return s.UpdateToken(id, in)
}
func (s *Store) DeleteOwnedToken(owner, id uint) error {
	if _, err := s.OwnedToken(owner, id); err != nil {
		return err
	}
	return s.DeleteToken(id)
}
func (s *Store) EnsureActiveOwner(id uint) error {
	u, err := s.GetUser(id)
	if err != nil || u.Status != model.UserStatusActive {
		return fmt.Errorf("owner must be active")
	}
	return nil
}

func (s *Store) UsageForUser(owner uint) (*LogSummary, error) {
	if owner == 0 {
		return nil, ErrNotFound
	}
	var summary LogSummary
	err := s.db.Model(&model.RequestLog{}).Where("user_id = ?", owner).Select(`
        COUNT(*) AS total_requests,
        COALESCE(SUM(CASE WHEN status = 200 THEN 1 ELSE 0 END),0) AS success_requests,
        COALESCE(SUM(CASE WHEN status <> 200 THEN 1 ELSE 0 END),0) AS error_requests,
        COALESCE(SUM(prompt_tokens),0) AS prompt_tokens,
        COALESCE(SUM(completion_tokens),0) AS completion_tokens,
        COALESCE(SUM(total_tokens),0) AS total_tokens,
        COALESCE(SUM(cost),0) AS total_cost`).Scan(&summary).Error
	return &summary, err
}
