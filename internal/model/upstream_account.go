package model

import "time"

// ManagedUpstreamAccount is independent of routing credentials and finances.
// TokenCiphertext contains the complete account-bound authenticated envelope.
type ManagedUpstreamAccount struct {
	ID                 string    `gorm:"primaryKey;size:35"`
	Provider           string    `gorm:"not null;size:32"`
	Name               string    `gorm:"not null;size:128"`
	Issuer             string    `gorm:"not null;size:512"`
	Subject            string    `gorm:"not null;size:512"`
	ClientID           string    `gorm:"not null;size:512"`
	RowRevision        int64     `gorm:"not null"`
	CredentialRevision int64     `gorm:"not null"`
	Fence              int64     `gorm:"not null"`
	TokenCiphertext    []byte    `gorm:"not null"`
	State              string    `gorm:"not null;size:16"`
	HasRefresh         bool      `gorm:"not null"`
	ScopesKnown        bool      `gorm:"not null"`
	ScopesJSON         string    `gorm:"not null;size:8192"`
	ExpiresAt          time.Time `gorm:"not null"`
	NextRefreshAt      time.Time `gorm:"not null"`
	LastFailure        string    `gorm:"not null;size:16"`
	RetryCount         int       `gorm:"not null"`
	ClaimHash          string    `gorm:"not null;size:64"`
	ClaimUntil         time.Time `gorm:"not null;index"`
	RefreshStarted     bool      `gorm:"not null"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

func (ManagedUpstreamAccount) TableName() string { return "managed_upstream_accounts" }
