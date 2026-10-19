package model

import "time"

// InferenceEligibility is one bounded, non-secret gate per managed credential.
// It is independent of account state and survives access-token rotation.
type InferenceEligibility struct {
	CredentialID               uint       `gorm:"primaryKey;autoIncrement:false"`
	Credential                 Credential `gorm:"foreignKey:CredentialID;references:ID;constraint:OnDelete:CASCADE"`
	AccountID                  string     `gorm:"not null;size:35"`
	BindingRevision            int64      `gorm:"not null"`
	Model                      string     `gorm:"not null;size:128"`
	GateEpoch                  int64      `gorm:"not null"`
	Revision                   int64      `gorm:"not null"`
	Blocked                    bool       `gorm:"not null"`
	Reason                     string     `gorm:"not null;size:32"`
	Scope                      string     `gorm:"not null;size:16"`
	ProviderCode               string     `gorm:"not null;size:64"`
	HTTPStatus                 int        `gorm:"not null"`
	Source                     string     `gorm:"not null;size:32"`
	ObservedAt                 *time.Time
	EvidenceAccountRevision    int64 `gorm:"not null"`
	EvidenceCredentialRevision int64 `gorm:"not null"`
	ResetKnown                 bool  `gorm:"not null"`
	ResetAt                    *time.Time
}
