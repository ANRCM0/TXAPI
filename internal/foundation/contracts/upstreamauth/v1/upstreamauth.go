// Package upstreamauth defines candidate operator account and OAuth boundaries.
package upstreamauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

const Version = "1"

const (
	MaxTokenBytes    = 16 * 1024
	MaxEnvelopeBytes = 64 * 1024
	MaxRequestBytes  = 32 * 1024
	MaxResponseBytes = 64 * 1024
)

var (
	ErrInvalid            = errors.New("invalid upstream account operation")
	ErrUnavailable        = errors.New("upstream account operation unavailable")
	ErrNotFound           = errors.New("upstream account not found")
	ErrConflict           = errors.New("upstream account revision or claim changed")
	ErrEncryptionRequired = errors.New("upstream accounts require credential encryption")
	ErrIdentityMismatch   = errors.New("upstream account identity does not match")
	ErrBusy               = errors.New("upstream account refresh capacity unavailable")
	ErrBackoff            = errors.New("upstream account refresh is deferred")
	ErrReauthorize        = errors.New("upstream account requires reauthorization")
	ErrUncertain          = errors.New("upstream credential exchange may have completed; reauthorize before retrying")
	ErrRejected           = errors.New("upstream authorization was rejected")
	ErrCallbackRejected   = errors.New("upstream authorization callback was rejected")
)

// Secret deliberately requires explicit access at the wire/encryption boundary.
type Secret struct{ value string }

func NewSecret(value string) Secret { return Secret{value: value} }
func (s Secret) Value() string      { return s.value }
func (s Secret) String() string     { return "[REDACTED]" }
func (s Secret) GoString() string   { return "upstreamauth.Secret([REDACTED])" }
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

type State string

const (
	Active      State = "active"
	Disabled    State = "disabled"
	Reauthorize State = "reauthorize"
	Uncertain   State = "uncertain"
)

type Failure string

const (
	NoFailure       Failure = ""
	NotSentFailure  Failure = "not_sent"
	RejectedFailure Failure = "rejected"
	UnknownFailure  Failure = "unknown"
)

type Identity struct {
	Issuer  string
	Subject string
}

type Account struct {
	ID                 string    `json:"id"`
	Provider           string    `json:"provider"`
	Name               string    `json:"name"`
	Issuer             string    `json:"issuer"`
	Subject            string    `json:"subject"`
	ClientID           string    `json:"client_id"`
	Revision           int64     `json:"revision,string"`
	CredentialRevision int64     `json:"credential_revision,string"`
	State              State     `json:"state"`
	HasRefresh         bool      `json:"has_refresh"`
	ScopesKnown        bool      `json:"scopes_known"`
	Scopes             []string  `json:"scopes"`
	ExpiresAt          time.Time `json:"expires_at"`
	NextRefreshAt      time.Time `json:"next_refresh_at"`
	LastFailure        Failure   `json:"last_failure"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Page struct {
	Data    []Account `json:"data"`
	HasMore bool      `json:"has_more"`
}

type Tokens struct {
	AccessToken  Secret
	RefreshToken Secret
	IDToken      Secret
	Nonce        Secret
	ExpiresAt    time.Time
	ScopesKnown  bool
	Scopes       []string
}

type Credential struct {
	Account Account
	Tokens  Tokens
}

type AuthorizationWrite struct {
	ID               string
	ExpectedRevision int64
	Provider         string
	Name             string
	ClientID         string
	Identity         Identity
	Tokens           Tokens
}

type Claim struct {
	AccountID          string
	Revision           int64
	CredentialRevision int64
	Fence              int64
	Proof              Secret
	LeaseUntil         time.Time
}

// ClaimRefresh returns no tokens. StartRefresh commits its dispatch marker
// before returning material; callers must not retry an uncertain started claim.
type Repository interface {
	HostID(context.Context) (string, error)
	List(context.Context, int, int) (Page, error)
	Read(context.Context, string) (Account, error)
	BeginAuthorization(context.Context, string, int64) (Account, error)
	SaveAuthorization(context.Context, AuthorizationWrite) (Account, error)
	ClaimRefresh(context.Context, string, int64) (Claim, error)
	StartRefresh(context.Context, Claim) (Credential, error)
	CompleteRefresh(context.Context, Claim, Tokens) (Account, error)
	FinishRefresh(context.Context, Claim, Failure) (Account, error)
	Disable(context.Context, string, int64) (Account, error)
	Recover(context.Context, int) (int, error)
}

type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   Secret
}

type Response struct {
	Phase    execution.Phase
	Status   int
	Header   http.Header
	Body     Secret
	Complete bool
}

type HTTPCaller interface {
	Call(context.Context, Request) Response
	Close()
}

type Flow struct {
	ClientID    string
	HostID      string
	RedirectURI string
	State       Secret
	Nonce       Secret
	Verifier    Secret
	URL         Secret
	ExpiresAt   time.Time
}

type Code struct {
	Value    Secret
	ClientID string
}

type IdentityExpectation struct {
	ClientID     string
	Subject      string
	Nonce        Secret
	AccessToken  Secret
	RequireNonce bool
	Now          time.Time
}

// Provider prepares and validates values only. The host owns all network I/O.
type Provider interface {
	Name() string
	Begin(clientID, hostID, redirectURI string, now time.Time) (Flow, error)
	Callback(Flow, string) (Code, error)
	CodeRequest(Flow, Code) (Request, error)
	RefreshRequest(string, Secret) (Request, error)
	KeysRequest() Request
	ReadTokens(Response, time.Time) (Tokens, error)
	VerifyIdentity(Secret, Secret, IdentityExpectation) (Identity, error)
}

type CallbackSession interface {
	RedirectURI() string
	Bind(Flow) error
	Wait(context.Context) (Code, error)
	Close() error
}

type Receiver interface {
	Open(context.Context) (CallbackSession, error)
}

type PresentAuthorization func(Secret) error

type Manager interface {
	Authorize(context.Context, string, string, int64, PresentAuthorization) (Account, error)
	List(context.Context, int, int) (Page, error)
	Refresh(context.Context, string, int64) (Account, error)
	Disable(context.Context, string, int64) (Account, error)
}
