package model

import state "minigateway/internal/foundation/contracts/state/v1"

var (
	ErrIntentConflict = state.ErrIntentConflict
	ErrIntentCapacity = state.ErrIntentCapacity
	ErrIntentNotFound = state.ErrIntentNotFound
	ErrIntentUnsafe   = state.ErrIntentUnsafe
	ErrClaimLost      = state.ErrClaimLost
)

const (
	IntentQueued     = "queued"
	IntentClaimed    = "claimed"
	IntentRunning    = "running"
	IntentFinalizing = "finalizing"
	IntentSucceeded  = "succeeded"
	IntentFailed     = "failed"
	IntentCancelled  = "cancelled"
	IntentExpired    = "expired"
	IntentUncertain  = "uncertain"
)

// GenerationIntent binds durable work to the original financial reservation.
// ClaimHash is not the claim capability; callers never persist its raw token.
type GenerationIntent struct {
	ID                    string `gorm:"primaryKey;size:64"`
	UserID                uint   `gorm:"not null;uniqueIndex:idx_intent_admission,priority:1;index:idx_intent_owner"`
	TokenID               uint   `gorm:"not null;uniqueIndex:idx_intent_admission,priority:2;index:idx_intent_owner"`
	AdmissionKey          string `gorm:"not null;size:64;uniqueIndex:idx_intent_admission,priority:3"`
	RequestDigest         string `gorm:"not null;size:64"`
	DigestVersion         string `gorm:"not null;size:64"`
	Operation             string `gorm:"not null;size:16"`
	Model                 string `gorm:"not null"`
	BillingID             string `gorm:"not null;size:32;uniqueIndex"`
	State                 string `gorm:"not null;index:idx_intent_deadline,priority:1"`
	Ciphertext            []byte `gorm:"not null"`
	CreatedAt             int64  `gorm:"not null;autoCreateTime:false"`
	Deadline              int64  `gorm:"not null;index:idx_intent_deadline,priority:2"`
	ExpiresAt             int64  `gorm:"not null;index"`
	ProviderPolicyVersion string `gorm:"not null;default:'';size:64"`
	ClaimEpoch            int64  `gorm:"not null;default:0"`
	ClaimHash             string `gorm:"not null;default:'';size:64"`
	ClaimUntil            int64  `gorm:"not null;default:0;index"`
	CancelRequested       bool   `gorm:"not null;default:false"`
	OutputRecordID        string `gorm:"not null;default:'';size:64"`
}

// GenerationClaim is an in-process capability for one fenced ownership epoch.
// It carries neither a Principal snapshot nor permission to bypass cost checks.
type GenerationClaim = state.GenerationClaim

// GenerationKeyRequirement contains only a content-format/key-ID header and an
// optional digest-key fingerprint, never request, output, or secret key bytes.
type GenerationKeyRequirement = state.GenerationKeyRequirement

type GenerationIntentLimits = state.GenerationIntentLimits
