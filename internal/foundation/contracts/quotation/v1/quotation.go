// Package quotation defines read-only selling calculations, never financial authority.
package quotation

import (
	"context"
	"errors"

	execution "minigateway/internal/foundation/contracts/execution/v3"
)

const Version = "1"

var (
	ErrInvalid      = errors.New("invalid quotation input")
	ErrNotFound     = errors.New("quotation version not found")
	ErrUnavailable  = errors.New("quotation unavailable")
	ErrOverflow     = errors.New("quotation amount exceeds range")
	ErrUnauthorized = errors.New("quotation session required")
	ErrForbidden    = errors.New("quotation administrator required")
	ErrRateLimited  = errors.New("quotation rate exceeded")
)

type Access struct{ Session string }

type Input struct {
	PriceVersion string
	Model        string
	Group        string
	Usage        execution.UsageObservation
}

type Current struct {
	Enabled      bool   `json:"enabled"`
	Version      string `json:"version"`
	Currency     string `json:"currency"`
	PriceProfile string `json:"price_profile"`
	UsageProfile string `json:"usage_profile"`
	UnitScale    string `json:"unit_scale"`
}

type Quote struct {
	PriceVersion string `json:"price_version"`
	Model        string `json:"model"`
	Group        string `json:"group"`
	Currency     string `json:"currency"`
	PriceProfile string `json:"price_profile"`
	UsageProfile string `json:"usage_profile"`
	UnitScale    string `json:"unit_scale"`
	AmountUnits  string `json:"amount_units"`
	Reason       string `json:"reason"`
	Known        bool   `json:"known"`
}

// Reader is an injected framework capability. It accepts trusted typed input,
// not proof that any input counts came from a particular execution.
type Reader interface {
	Current(context.Context) (Current, error)
	Calculate(context.Context, Input) (Quote, error)
}

type Service interface {
	Current(context.Context, Access) (Current, error)
	Calculate(context.Context, Access, Input) (Quote, error)
}
