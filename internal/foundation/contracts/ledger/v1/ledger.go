// Package ledger defines audited read and resolution capabilities.
package ledger

import (
	"context"
	"errors"
	"time"
)

const Version = "1"
const UnitScale int64 = 1_000_000_000
const (
	Reserved     = "reserved"
	Active       = "active"
	Pending      = "pending"
	Settled      = "settled"
	ResolveUsage = "settle_usage"
	ResolveCap   = "cap_usage"
	ResolveWaive = "waive"
)

var (
	ErrStrictUnavailable  = errors.New("strict billing is unavailable")
	ErrSettlementConflict = errors.New("conflicting settlement for the same reservation")
	ErrLedgerNotFound     = errors.New("billing reservation not found")
	ErrLedgerInput        = errors.New("invalid billing query or resolution")
	ErrNotResolvable      = errors.New("only pending reservations with expired leases can be resolved")
	ErrCostExceedsHold    = errors.New("verified cost exceeds hold; explicitly cap the charge or waive it")
	ErrBillingForbidden   = errors.New("active administrator required")
)

type ReservationView struct {
	ID                  string    `json:"id"`
	UserID              uint      `json:"user_id"`
	TokenID             uint      `json:"token_id"`
	PriceVersion        string    `json:"price_version"`
	OriginModel         string    `json:"origin_model"`
	ReservedUnits       string    `json:"reserved_units"`
	ChargeUnits         string    `json:"charge_units"`
	ProviderBudgetUnits string    `json:"provider_budget_units"`
	ProviderHeldUnits   string    `json:"provider_held_units"`
	State               string    `json:"state"`
	Reason              string    `json:"reason"`
	LeaseUntil          int64     `json:"lease_until"`
	CanResolve          bool      `json:"can_resolve"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}
type AttemptView struct {
	Number                   int       `json:"number"`
	ChannelID                uint      `json:"channel_id"`
	CredentialID             uint      `json:"credential_id"`
	ActualModel              string    `json:"actual_model"`
	Group                    string    `json:"group"`
	Status                   int       `json:"status"`
	Termination              string    `json:"termination"`
	PromptTokens             int       `json:"prompt_tokens"`
	OutputTokens             int       `json:"output_tokens"`
	CostUnits                string    `json:"cost_units"`
	CostKnown                bool      `json:"cost_known"`
	ProviderBoundUnits       string    `json:"provider_bound_units"`
	ProviderContractVersion  string    `json:"provider_contract_version"`
	ProviderContractSnapshot string    `json:"provider_contract_snapshot"`
	State                    string    `json:"state"`
	CreatedAt                time.Time `json:"created_at"`
}
type ResolutionView struct {
	ActorID         uint      `json:"actor_id"`
	Action          string    `json:"action"`
	Reason          string    `json:"reason"`
	Evidence        string    `json:"evidence"`
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CostKnown       bool      `json:"cost_known"`
	CalculatedUnits string    `json:"calculated_units"`
	ChargeUnits     string    `json:"charge_units"`
	CreatedAt       time.Time `json:"created_at"`
}
type ReservationDetail struct {
	Reservation   ReservationView `json:"reservation"`
	Attempts      []AttemptView   `json:"attempts"`
	Resolution    *ResolutionView `json:"resolution"`
	PriceSnapshot string          `json:"price_snapshot"`
}
type LedgerQuery struct {
	State           string
	UserID, TokenID uint
	Limit, Offset   int
}
type ResolutionInput struct {
	Action   string           `json:"action"`
	Reason   string           `json:"reason"`
	Evidence string           `json:"evidence"`
	Usage    *ResolutionUsage `json:"usage,omitempty"`
}
type ResolutionUsage struct {
	PromptTokens *int `json:"prompt_tokens"`
	OutputTokens *int `json:"completion_tokens"`
}
type Service interface {
	ListReservations(context.Context, LedgerQuery) ([]ReservationView, error)
	ReservationDetail(context.Context, string) (*ReservationDetail, error)
	ResolveReservation(context.Context, string, uint, ResolutionInput) (*ResolutionView, error)
}
