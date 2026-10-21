package model

import "time"

// Public configuration bounds leave room for safe fixed-point conversion.
const (
	MaxPricePer1K = 1_000_000_000
	MaxGroupRate  = 1_000_000
)

// BillingPriceVersion is append-only configuration history, not a settlement
// ledger. Ordinary log retention must not delete these immutable snapshots.
type BillingPriceVersion struct {
	Version   string    `gorm:"primaryKey;size:64" json:"version"`
	Payload   string    `gorm:"type:text;not null" json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type BillingBalance struct {
	OwnerType     string `gorm:"primaryKey;size:8"`
	OwnerID       uint   `gorm:"primaryKey"`
	UsedUnits     int64  `gorm:"not null;default:0"`
	ReservedUnits int64  `gorm:"not null;default:0"`
}

type BillingReservation struct {
	ID                    string `gorm:"primaryKey;size:32"`
	UserID                uint   `gorm:"index;not null"`
	TokenID               uint   `gorm:"index;not null"`
	PriceVersion          string `gorm:"size:64;not null"`
	OriginModel           string `gorm:"not null"`
	ReservedUnits         int64  `gorm:"not null"`
	ProviderBudgetUnits   int64  `gorm:"not null;default:0"`
	ProviderPolicyVersion string `gorm:"size:64;not null;default:''"`
	ProviderHeldUnits     int64  `gorm:"not null;default:0"`
	ChargeUnits           int64  `gorm:"not null;default:0"`
	State                 string `gorm:"index;not null"`
	Reason                string
	Fingerprint           string `gorm:"size:64"`
	LeaseUntil            int64  `gorm:"index;not null"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type BillingAttempt struct {
	ReservationID            string `gorm:"primaryKey;size:32"`
	Number                   int    `gorm:"primaryKey"`
	ChannelID                uint
	CredentialID             uint
	ActualModel              string
	Group                    string
	Status                   int
	Termination              string
	PromptTokens             int
	OutputTokens             int
	CostUnits                int64
	CostKnown                bool
	ProviderBoundUnits       int64  `gorm:"not null;default:0"`
	ProviderContractVersion  string `gorm:"size:64;not null;default:''"`
	ProviderContractSnapshot string `gorm:"type:text;not null;default:''"`
	State                    string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// BillingResolution is the immutable, one-per-reservation administrator decision.
// It is committed in the same transaction as both balance changes.
type BillingResolution struct {
	ReservationID   string `gorm:"primaryKey;size:32"`
	ActorID         uint   `gorm:"not null"`
	Action          string `gorm:"size:24;not null"`
	Reason          string `gorm:"type:text;not null"`
	Evidence        string `gorm:"type:text;not null"`
	PromptTokens    int
	OutputTokens    int
	CostKnown       bool
	CalculatedUnits int64
	ChargeUnits     int64
	Fingerprint     string `gorm:"size:64;not null"`
	CreatedAt       time.Time
}
