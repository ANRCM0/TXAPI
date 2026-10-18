// Package execution defines the versioned generation boundary without implementation types.
package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	identity "minigateway/internal/foundation/contracts/identity/v1"
)

const Version = "1"

var ErrDeadlineUnsupported = http.ErrNotSupported

type Operation string

const (
	Chat       Operation = "chat"
	Embeddings Operation = "embeddings"
	Responses  Operation = "responses"
)

type TargetConstraint struct {
	ChannelID    uint   `json:"channel_id"`
	CredentialID uint   `json:"credential_id"`
	ActualModel  string `json:"actual_model"`
	Fingerprint  string `json:"fingerprint"`
}

type Invocation struct {
	Operation Operation
	Body      []byte
	SessionID string
	OutputID  string
	Target    *TargetConstraint
}

type Phase string

const (
	NotDispatched    Phase = "not_dispatched"
	Dispatching      Phase = "dispatching"
	ResponseReceived Phase = "response_received"
)

type Termination string

const (
	Success          Termination = "success"
	UpstreamError    Termination = "upstream_error"
	Deadline         Termination = "deadline"
	Canceled         Termination = "canceled"
	ClientWriteError Termination = "client_write_error"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ErrorDetail struct {
	Message string          `json:"message"`
	Type    string          `json:"type,omitempty"`
	Code    json.RawMessage `json:"code,omitempty"`
}
type Error struct {
	StatusCode int
	Error      ErrorDetail
}

type Result struct {
	Phase            Phase
	UpstreamStatus   int
	ProtocolComplete bool
	StatusCode       int
	HTTPStatus       int
	Committed        bool
	Replayable       bool
	Termination      Termination
	Usage            *Usage
	Error            *Error
	Duration         time.Duration
	BytesWritten     int64
}

type ResponseMetadata struct {
	StatusCode  int
	ContentType string
	Values      map[string]string
}
type OutputState struct {
	Committed  bool
	HTTPStatus int
}
type Output interface {
	Commit(context.Context, ResponseMetadata) error
	Write(context.Context, []byte) (int, error)
	Flush(context.Context) error
	State() OutputState
}
type DeadlineOutput interface {
	Output
	SetWriteDeadline(time.Time) error
}

type Identity struct {
	Policy        *identity.Policy
	TokenID       uint
	UserID        uint
	TokenName     string
	RequestID     string
	AllowedModels []string
}
type AttemptOutcome struct {
	Operation    Operation
	ChannelID    uint
	CredentialID uint
	Model        string
	Result
}
type NativeCompletion struct{ Status, ID string }
type Outcome struct {
	Target        TargetConstraint
	Native        NativeCompletion
	Operation     Operation
	BillingState  string
	BillingID     string
	PriceVersion  string
	Identity      Identity
	ChannelID     uint
	CredentialID  uint
	ChannelName   string
	ActualModel   string
	ChannelGroup  string
	ErrorMessage  string
	Attempts      []AttemptOutcome
	TotalDuration time.Duration
	Result
}

// Execute performs one generation. Identity comes from the trusted context,
// not from the invocation, output, result or caller-provided binding IDs.
type Executor interface {
	Execute(context.Context, Output, Invocation) Outcome
}
