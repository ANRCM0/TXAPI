package model

import "errors"

var (
	ErrIntentConflict = errors.New("generation intent conflicts with its idempotency scope")
	ErrIntentCapacity = errors.New("generation intent capacity exceeded")
	ErrIntentNotFound = errors.New("generation intent was not found")
	ErrIntentUnsafe   = errors.New("generation intent cannot be safely released")
)

const (
	IntentQueued    = "queued"
	IntentCancelled = "cancelled"
	IntentExpired   = "expired"
	IntentUncertain = "uncertain"
)

// GenerationIntent records atomic admission, not an upstream task receipt. This
// first slice deliberately has no dispatch/claim transition or replay handle.
type GenerationIntent struct {
	ID            string `gorm:"primaryKey;size:64"`
	UserID        uint   `gorm:"not null;uniqueIndex:idx_intent_admission,priority:1;index:idx_intent_owner"`
	TokenID       uint   `gorm:"not null;uniqueIndex:idx_intent_admission,priority:2;index:idx_intent_owner"`
	AdmissionKey  string `gorm:"not null;size:64;uniqueIndex:idx_intent_admission,priority:3"`
	RequestDigest string `gorm:"not null;size:64"`
	DigestVersion string `gorm:"not null;size:64"`
	Operation     string `gorm:"not null;size:16"`
	Model         string `gorm:"not null"`
	BillingID     string `gorm:"not null;size:32;uniqueIndex"`
	State         string `gorm:"not null;index:idx_intent_deadline,priority:1"`
	Ciphertext    []byte `gorm:"not null"`
	CreatedAt     int64  `gorm:"not null;autoCreateTime:false"`
	Deadline      int64  `gorm:"not null;index:idx_intent_deadline,priority:2"`
	ExpiresAt     int64  `gorm:"not null;index"`
}

type GenerationIntentLimits struct {
	InstanceRecords int64
	OwnerRecords    int64
	InstanceBytes   int64
	OwnerBytes      int64
}
