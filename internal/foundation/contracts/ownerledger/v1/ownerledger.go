// Package ownerledger defines current-owner financial reads without mutation authority.
package ownerledger

import (
	"context"
	"errors"
	"time"
)

const Version = "1"

var (
	ErrInvalid      = errors.New("invalid owner ledger query")
	ErrUnauthorized = errors.New("valid owner ledger session required")
	ErrNotFound     = errors.New("owner billing reservation not found")
	ErrUnavailable  = errors.New("owner ledger unavailable")
	ErrRateLimited  = errors.New("owner ledger query rate exceeded")
)

// Access grants no supplied owner or administrator scope.
type Access struct {
	Session string
}

type Query struct {
	State         string
	TokenID       uint
	Limit, Offset int
}

// PostedUsedUnits includes any opening balance imported by the existing ledger.
// Missing balances remain unknown; user and key holds must never be added together.
type Summary struct {
	UnitScale       string  `json:"unit_scale"`
	BalanceKnown    bool    `json:"balance_known"`
	PostedUsedUnits *string `json:"posted_used_units"`
	HeldUnits       *string `json:"held_units"`
	PendingCount    string  `json:"pending_count"`
	AsOf            int64   `json:"as_of"`
}

// OriginalReservedUnits is historical. ChargeUnits is unknown until settled,
// while an expired lease alone never releases HeldUnits.
type Reservation struct {
	ID                    string    `json:"id"`
	TokenID               uint      `json:"token_id"`
	OriginModel           string    `json:"origin_model"`
	PriceVersion          string    `json:"price_version"`
	State                 string    `json:"state"`
	OriginalReservedUnits string    `json:"original_reserved_units"`
	HeldUnits             string    `json:"held_units"`
	ChargeUnits           *string   `json:"charge_units"`
	LeaseUntil            int64     `json:"lease_until"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type Detail struct {
	Reservation      Reservation `json:"reservation"`
	ResolutionAction string      `json:"resolution_action,omitempty"`
}

type Page struct {
	Data    []Reservation `json:"data"`
	HasMore bool          `json:"has_more"`
}

type Repository interface {
	Summary(context.Context, Access) (Summary, error)
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Detail, error)
}

type Service interface {
	Summary(context.Context, Access) (Summary, error)
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Detail, error)
}
