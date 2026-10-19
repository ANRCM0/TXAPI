package model

import history "minigateway/internal/foundation/contracts/usagehistory/v1"

// UsageInvocation contains bounded execution metadata, never content or financial authority.
type UsageInvocation struct {
	ID                string            `gorm:"primaryKey;size:38"`
	UserID            uint              `gorm:"not null;index:idx_usage_owner,priority:1"`
	TokenID           uint              `gorm:"not null;index"`
	RequestID         string            `gorm:"size:64;not null"`
	Operation         string            `gorm:"size:32;not null"`
	Model             string            `gorm:"size:256;not null"`
	Stream            bool              `gorm:"not null"`
	QuotationVersion  string            `gorm:"size:64;not null"`
	MaxAttempts       int               `gorm:"not null"`
	AttemptCount      int               `gorm:"not null"`
	State             string            `gorm:"size:16;not null;index:idx_usage_expiry,priority:1"`
	CreatedAt         int64             `gorm:"not null;autoCreateTime:false;index:idx_usage_owner,priority:2"`
	FinishedAt        int64             `gorm:"not null"`
	ExpiresAt         int64             `gorm:"not null;index:idx_usage_expiry,priority:2"`
	Host              history.HostFacts `gorm:"embedded;embeddedPrefix:host_"`
	StartFingerprint  string            `gorm:"size:64;not null"`
	FinishFingerprint string            `gorm:"size:64;not null"`
}

type UsageAttemptReceipt struct {
	InvocationID          string            `gorm:"primaryKey;size:38;autoIncrement:false"`
	Number                int               `gorm:"primaryKey;autoIncrement:false"`
	ChannelID             uint              `gorm:"not null"`
	CredentialID          uint              `gorm:"not null"`
	ActualModel           string            `gorm:"size:256;not null"`
	Group                 string            `gorm:"size:128;not null"`
	Protocol              string            `gorm:"size:64;not null"`
	State                 string            `gorm:"size:16;not null"`
	MayDispatch           bool              `gorm:"not null"`
	Host                  history.HostFacts `gorm:"embedded;embeddedPrefix:host_"`
	InputTokens           *int64
	OutputTokens          *int64
	TotalTokens           *int64
	CachedInputTokens     *int64
	CacheWriteInputTokens *int64
	ReasoningOutputTokens *int64
	Completeness          string `gorm:"size:16;not null"`
	Source                string `gorm:"size:32;not null"`
	Profile               string `gorm:"size:64;not null"`
	PrepareFingerprint    string `gorm:"size:64;not null"`
	FinishFingerprint     string `gorm:"size:64;not null"`
}

type UsageHistoryLimits struct {
	InstanceRecords  int
	OwnerRecords     int
	RetentionSeconds int
}
