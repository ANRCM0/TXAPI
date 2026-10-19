// Package upstreamauth defines the candidate eligibility ports approved by
// FC-P3-001. Registration does not freeze this API.
package upstreamauth

import (
	"context"
	"errors"
	"time"

	v2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
)

const Version = "3"

var (
	ErrBlocked  = errors.New("managed inference eligibility is blocked")
	ErrCapacity = errors.New("managed inference eligibility capacity exceeded")
)

type Reason string

const (
	AuthorizationReview Reason = "authorization_review"
	UsageLimited        Reason = "usage_limited"
	RateLimitUnknown    Reason = "rate_limit_unknown"
)

type Scope string

const (
	Grant Scope = "grant"
	Model Scope = "model"
)

type Source string

const (
	ModelsHTTP     Source = "models_http"
	ResponsesHTTP  Source = "responses_http"
	ResponsesEvent Source = "responses_event"
)

// Signals contain fixed classifications only, never request or error text.
type FailureSignal struct {
	Reason       Reason
	Scope        Scope
	ProviderCode string
	HTTPStatus   int
	Source       Source
}

type FailureWrite struct {
	Issued    v2.AccessRequest
	GateEpoch int64
	Signal    FailureSignal
}

type Eligibility struct {
	CredentialID               uint       `json:"credential_id"`
	AccountID                  string     `json:"account_id"`
	BindingRevision            int64      `json:"binding_revision,string"`
	Model                      string     `json:"model"`
	GateEpoch                  int64      `json:"gate_epoch,string"`
	Revision                   int64      `json:"revision,string"`
	Blocked                    bool       `json:"blocked"`
	Reason                     Reason     `json:"reason"`
	Scope                      Scope      `json:"scope"`
	ProviderCode               string     `json:"provider_code"`
	HTTPStatus                 int        `json:"http_status"`
	Source                     Source     `json:"source"`
	ObservedAt                 *time.Time `json:"observed_at"`
	EvidenceAccountRevision    int64      `json:"evidence_account_revision,string"`
	EvidenceCredentialRevision int64      `json:"evidence_credential_revision,string"`
	ResetKnown                 bool       `json:"reset_known"`
	ResetAt                    *time.Time `json:"reset_at"`
}

type RecordResult struct {
	Applied     bool
	Eligibility Eligibility
}

type ResumeRequest struct {
	CredentialID            uint  `json:"credential_id"`
	ExpectedBindingRevision int64 `json:"expected_binding_revision,string"`
	ExpectedGateRevision    int64 `json:"expected_gate_revision,string"`
	ExpectedGateEpoch       int64 `json:"expected_gate_epoch,string"`
}

type FailureRecorder interface {
	RecordFailure(context.Context, FailureWrite) (RecordResult, error)
}

// The optional L2 observer describes a validated, complete native error event.
type FailureObserver interface {
	InferenceFailure() (FailureSignal, bool)
}

type EligibilityAdmin interface {
	ReadEligibility(context.Context, uint) (Eligibility, error)
	ResumeEligibility(context.Context, ResumeRequest) (Eligibility, error)
}

type EligibilityRepository interface {
	FailureRecorder
	EligibilityAdmin
}
