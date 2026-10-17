package model

import "errors"

var (
	ErrResponseConflict    = errors.New("response request conflicts with its idempotency scope")
	ErrResponseCapacity    = errors.New("response request capacity exceeded")
	ErrResponseUnavailable = errors.New("response request persistence is unavailable")
)

const (
	ResponseDispatching = "dispatching"
	ResponseCompleted   = "completed"
	ResponseFailed      = "failed"
	ResponseUncertain   = "uncertain"
	ResponseRejected    = "rejected"
)

// ResponseRequest stores idempotency facts, never plaintext keys or content.
// Unknown execution protects its scope even after the advertised replay TTL.
type ResponseRequest struct {
	ID                 string `gorm:"primaryKey;size:64"`
	UserID             uint   `gorm:"not null;uniqueIndex:idx_response_scope,priority:1;index:idx_response_owner"`
	TokenID            uint   `gorm:"not null;uniqueIndex:idx_response_scope,priority:2"`
	Operation          string `gorm:"not null;size:16;uniqueIndex:idx_response_scope,priority:3"`
	KeyHash            string `gorm:"not null;size:64;uniqueIndex:idx_response_scope,priority:4"`
	RequestHash        string `gorm:"not null;size:64"`
	DigestVersion      string `gorm:"not null;size:64"`
	ResponseID         string `gorm:"not null;size:70;uniqueIndex"`
	UpstreamResponseID string `gorm:"not null;default:'';size:256"`
	Model              string `gorm:"not null;size:256"`
	Stream             bool   `gorm:"not null"`
	Store              bool   `gorm:"not null"`
	State              string `gorm:"not null;index:idx_response_expiry,priority:1"`
	NativeStatus       string `gorm:"not null;default:'';size:32"`
	OutputRecordID     string `gorm:"not null;default:'';size:64"`
	StatusCode         int    `gorm:"not null;default:0"`
	HTTPStatus         int    `gorm:"not null;default:0"`
	BillingID          string `gorm:"not null;default:'';size:64"`
	ErrorType          string `gorm:"not null;default:'';size:64"`
	CreatedAt          int64  `gorm:"not null;autoCreateTime:false"`
	ExpiresAt          int64  `gorm:"not null;index:idx_response_expiry,priority:2"`
}

type ResponseRequestLimits struct {
	InstanceRecords int64
	OwnerRecords    int64
}

type ResponseRequestCompletion struct {
	State              string
	NativeStatus       string
	UpstreamResponseID string
	OutputRecordID     string
	StatusCode         int
	HTTPStatus         int
	BillingID          string
	ErrorType          string
}
