// Package responsehistory exposes the shared encrypted Responses history owner.
// This candidate v1 port is internal; registration does not freeze its API.
package responsehistory

import (
	"context"
	"encoding/json"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

const Version = "1"

// Content preserves the v1 encrypted document format. Raw documents are validated
// by the operation codec; this port does not infer readiness or permit replay.
type Content struct {
	Request      json.RawMessage         `json:"request,omitempty"`
	Ready        bool                    `json:"ready"`
	Active       string                  `json:"active,omitempty"`
	Input        []json.RawMessage       `json:"input,omitempty"`
	Items        []json.RawMessage       `json:"items,omitempty"`
	Response     json.RawMessage         `json:"response,omitempty"`
	Metadata     map[string]string       `json:"metadata,omitempty"`
	Parent       string                  `json:"parent,omitempty"`
	Conversation string                  `json:"conversation,omitempty"`
	Target       execv1.TargetConstraint `json:"target"`
	PriceVersion string                  `json:"price_version,omitempty"`
	BillingID    string                  `json:"billing_id,omitempty"`
}

// Repository owns content protection and the bounded persistence configuration.
// Read and Apply delegate current owner/Key/model authorization and CAS to the
// state owner. Callers must not derive trusted row identities from request data.
// Seal encrypts only; publication requires a successful Apply. Fresh creates an
// unpersisted identity with the configured TTL. No method dispatches inference.
type Repository interface {
	Read(context.Context, identity.Binding, string) (state.ResponseHistory, Content, error)
	Seal(state.ResponseHistory, Content) (state.ResponseHistory, error)
	Fresh(string, string, string, identity.Binding) state.ResponseHistory
	Apply(context.Context, identity.Binding, []state.HistoryChange, ...state.HistoryGuard) error
}
