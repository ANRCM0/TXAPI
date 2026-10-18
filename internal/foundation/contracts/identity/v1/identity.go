// Package identity defines read-only identity projections, never authentication proof.
package identity

import (
	"context"
	"errors"
	"strings"
)

const Version = "1"
const (
	All       = "all"
	Allowlist = "allowlist"
)

var ErrUnauthorized = errors.New("generation identity is no longer authorized")
var ErrUnavailable = errors.New("current identity cannot be verified")

type Binding struct {
	UserID  uint
	TokenID uint
}

type Policy struct {
	Mode   string   `json:"mode"`
	Models []string `json:"models"`
}

func (p Policy) Validate() error {
	if p.Mode != All && p.Mode != Allowlist {
		return errors.New("model_mode must be all or allowlist")
	}
	if p.Mode == All && len(p.Models) > 0 {
		return errors.New("all policy must not contain an allowlist")
	}
	for _, m := range p.Models {
		if strings.TrimSpace(m) == "" || m == "*" {
			return errors.New("allowlist requires explicit non-wildcard model names")
		}
	}
	return nil
}

func (p Policy) Allows(model string) bool {
	if p.Mode == All {
		return true
	}
	if p.Mode != Allowlist {
		return false
	}
	for _, allowed := range p.Models {
		if allowed == model {
			return true
		}
	}
	return false
}

// Principal is a copied permission snapshot. Constructing or changing one cannot
// authenticate a context; only the configured authority refreshes trusted state.
type Principal struct {
	UserID               uint
	TokenID              uint
	TokenName            string
	Policy               Policy
	RPMLimit             int
	TPMLimit             int
	UserRPMLimit         int
	UserConcurrencyLimit int
	KeyConcurrencyLimit  int
}

type Authority interface {
	Current(context.Context) (Principal, bool)
	Refresh(context.Context, Binding) (context.Context, Principal, error)
}
