// Package control defines explicit management capabilities and owned values.
package control

import (
	"errors"
	"fmt"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"time"
)

const Version = "1"
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var ErrNotFound = errors.New("\u8bb0\u5f55\u4e0d\u5b58\u5728")
var ErrForbidden = errors.New("cannot create a key for another user")

type User struct {
	ID               uint      `json:"id"`
	Username         string    `json:"username"`
	Role             string    `json:"role"`
	ModelMode        string    `json:"model_mode"`
	Models           []string  `json:"models"`
	RPMLimit         int       `json:"rpm_limit"`
	TPMLimit         int       `json:"tpm_limit"`
	ConcurrencyLimit int       `json:"concurrency_limit"`
	Quota            float64   `json:"quota"`
	UsedQuota        float64   `json:"used_quota"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type Token struct {
	ID               uint      `json:"id"`
	UserID           uint      `json:"user_id"`
	Name             string    `json:"name"`
	Key              string    `json:"key,omitempty"`
	KeyPrefix        string    `json:"key_prefix"`
	ModelMode        string    `json:"model_mode"`
	ConcurrencyLimit int       `json:"concurrency_limit"`
	Models           []string  `json:"models"`
	Quota            float64   `json:"quota"`
	UsedQuota        float64   `json:"used_quota"`
	RPMLimit         int       `json:"rpm_limit"`
	TPMLimit         int       `json:"tpm_limit"`
	ExpiresAt        int64     `json:"expires_at"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type RequestLog struct {
	ID               uint      `json:"id"`
	RequestID        string    `json:"request_id"`
	UserID           uint      `json:"user_id"`
	TokenID          uint      `json:"token_id"`
	ChannelID        uint      `json:"channel_id"`
	CredentialID     uint      `json:"credential_id"`
	OriginModel      string    `json:"origin_model"`
	ActualModel      string    `json:"actual_model"`
	IsStream         bool      `json:"is_stream"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	Cost             float64   `json:"cost"`
	PriceVersion     string    `json:"price_version"`
	BillingID        string    `json:"billing_id"`
	BillingState     string    `json:"billing_state"`
	LatencyMs        int64     `json:"latency_ms"`
	Status           int       `json:"status"`
	ErrorMessage     string    `json:"error_message"`
	IsEstimated      bool      `json:"is_estimated"`
	PriceMissing     bool      `json:"price_missing"`
	CreatedAt        time.Time `json:"created_at"`
}
type Ability struct {
	GroupName string `json:"group_name"`
	Model     string `json:"model"`
	ChannelID uint   `json:"channel_id"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
}
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
type TokenInput struct {
	Name             string   `json:"name"`
	Key              string   `json:"key"`
	UserID           uint     `json:"user_id"`
	ModelMode        string   `json:"model_mode"`
	Models           []string `json:"models"`
	ExpiresInSeconds int64    `json:"expires_in_seconds"`
	PreserveExpiry   bool     `json:"preserve_expiry"`
	Quota            float64  `json:"quota"`
	RPMLimit         int      `json:"rpm_limit"`
	TPMLimit         int      `json:"tpm_limit"`
	ConcurrencyLimit int      `json:"concurrency_limit"`
	Status           string   `json:"status"`
}
type LogQuery struct {
	UserID    uint
	OwnerOnly bool
	Limit     int
	Offset    int
	ChannelID uint
	TokenID   uint
	Model     string
}
type LogSummary struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessRequests  int64   `json:"success_requests"`
	ErrorRequests    int64   `json:"error_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
	EstimatedCount   int64   `json:"estimated_count"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
}
type ModelStat struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"success_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
	EstimatedCount   int64   `json:"estimated_count"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
}
type ChannelStat struct {
	ChannelID        uint    `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"success_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
}

func (u User) Policy() identity.Policy {
	return identity.Policy{Mode: u.ModelMode, Models: append([]string(nil), u.Models...)}
}

type ModelChannel struct {
	Enabled bool
	Models  []string
}
type ModelListing struct {
	Models []string
	Policy identity.Policy
}

type Accounts interface {
	Login(string, string) (string, *User, error)
	Logout(string) error
	SessionUser(string) (*User, error)
	ListUsers() ([]User, error)
	GetUser(uint) (*User, error)
	CreateUser(*UserInput) (*User, error)
	UpdateUser(uint, *UserInput) (*User, error)
	SystemPolicy() (identity.Policy, error)
	SetSystemPolicy(identity.Policy) error
}
type Keys interface {
	ListTokens() []Token
	IssueToken(uint, *TokenInput) (*Token, error)
	UpdateToken(uint, *TokenInput) (*Token, error)
	DeleteToken(uint) error
	SetTokenQuota(uint, float64) (*Token, error)
	ClaimToken(uint, uint) (*Token, error)
	OwnerTokens(uint) ([]Token, error)
	OwnedToken(uint, uint) (*Token, error)
	UpdateOwnedToken(uint, uint, *TokenInput) (*Token, error)
	DeleteOwnedToken(uint, uint) error
}
type Statistics interface {
	ListLogs(LogQuery) ([]RequestLog, error)
	UsageForUser(uint) (*LogSummary, error)
	LogSummary(time.Time) (*LogSummary, error)
	StatsByModel(time.Time) ([]ModelStat, error)
	StatsByChannel(time.Time) ([]ChannelStat, error)
	Abilities() ([]Ability, error)
}
type ModelCatalog interface{ ModelChannels() []ModelChannel }
type Repository interface {
	Accounts
	Keys
	Statistics
	ModelCatalog
}

// Service capabilities are injected only into the authorized management entry.
// DTO IDs are scope values, not credentials or permission to call privileged APIs.
type Service interface {
	Accounts
	Keys
	Statistics
	AllowLogin(string) bool
	ModelsForPolicy(identity.Policy) (ModelListing, error)
	CreateOwnedKey(uint, *TokenInput) (*Token, error)
}

func ParseSince(raw string, now time.Time) (time.Time, error) {
	switch raw {
	case "", "7d":
		return now.Add(-7 * 24 * time.Hour), nil
	case "all":
		return time.Time{}, nil
	}
	unit := raw[len(raw)-1]
	numStr := raw[:len(raw)-1]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("\u975e\u6cd5\u7684 range: %q\uff08\u652f\u6301 24h/7d/30d/all\uff09", raw)
	}
	switch unit {
	case 'h':
		return now.Add(-time.Duration(n) * time.Hour), nil
	case 'd':
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("\u975e\u6cd5\u7684 range \u5355\u4f4d: %q\uff08\u652f\u6301 h/d\uff09", raw)
}
