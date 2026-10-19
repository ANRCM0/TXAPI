// Package usagehistory describes durable observations without financial authority.
package usagehistory

import (
	"context"
	"errors"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	usage "minigateway/internal/foundation/contracts/execution/v3"
)

const Version = "1"

var (
	ErrInvalid      = errors.New("invalid usage history input")
	ErrUnauthorized = errors.New("valid usage history identity required")
	ErrForbidden    = errors.New("usage history scope forbidden")
	ErrNotFound     = errors.New("usage record not retained")
	ErrUnavailable  = errors.New("usage history unavailable")
	ErrConflict     = errors.New("usage record conflict")
	ErrCapacity     = errors.New("usage history capacity exhausted")
	ErrRateLimited  = errors.New("usage history query rate exceeded")
)

type Start struct {
	// Empty and http share the original HTTP fingerprint for replay compatibility.
	Transport        string `json:",omitempty"`
	ID               string
	RequestID        string
	Model            string
	QuotationVersion string
	Operation        execution.Operation
	Stream           bool
	MaxAttempts      int
}

type Target struct {
	ID           string
	ActualModel  string
	Group        string
	Protocol     string
	Number       int
	ChannelID    uint
	CredentialID uint
}

// HostFacts describes the attempt/coordination result, not client consumption.
type HostFacts struct {
	// ProtocolStatus is a native snapshot, independent of mechanical termination.
	ProtocolStatus    string                `json:"protocol_status,omitempty"`
	Phase             execution.Phase       `json:"phase"`
	Status            int                   `json:"status"`
	UpstreamStatus    int                   `json:"upstream_status"`
	HTTPStatus        int                   `json:"http_status"`
	Termination       execution.Termination `json:"termination"`
	ProtocolComplete  bool                  `json:"protocol_complete"`
	DeliveryCommitted bool                  `json:"delivery_committed"`
	BytesWritten      int64                 `json:"bytes_written,string"`
}

type Receipt struct {
	ID     string
	Number int
	Host   HostFacts
	Usage  usage.UsageObservation
}

type Completion struct {
	ID          string
	Host        HostFacts
	Interrupted bool
}

// Recorder is a trusted host capability. Replayed writes never authorize execution.
type Recorder interface {
	Begin(context.Context, Start) error
	Prepare(context.Context, Target) error
	MayDispatch(context.Context, string, int) error
	Observe(context.Context, Receipt) error
	Finish(context.Context, Completion) error
}

// Administrator requests a scope; the repository still verifies the current role.
type Access struct {
	Session       string
	Administrator bool
}

type Query struct {
	State            string
	OwnerID, TokenID uint
	Limit, Offset    int
}

type Summary struct {
	Transport        string              `json:"transport"`
	ID               string              `json:"id"`
	UserID           uint                `json:"user_id"`
	TokenID          uint                `json:"token_id"`
	RequestID        string              `json:"request_id"`
	Operation        execution.Operation `json:"operation"`
	Model            string              `json:"model"`
	Stream           bool                `json:"stream"`
	State            string              `json:"state"`
	CreatedAt        int64               `json:"created_at"`
	FinishedAt       int64               `json:"finished_at"`
	ExpiresAt        int64               `json:"expires_at"`
	AttemptCount     int                 `json:"attempt_count"`
	QuotationVersion string              `json:"quotation_version,omitempty"`
	Host             HostFacts           `json:"host"`
}

// Nil is unknown and a pointer to "0" is known zero; JSON never rounds counts.
type UsageView struct {
	InputTokens           *string `json:"input_tokens"`
	OutputTokens          *string `json:"output_tokens"`
	TotalTokens           *string `json:"total_tokens"`
	CachedInputTokens     *string `json:"cached_input_tokens"`
	CacheWriteInputTokens *string `json:"cache_write_input_tokens"`
	ReasoningOutputTokens *string `json:"reasoning_output_tokens"`
	Completeness          string  `json:"completeness"`
	Source                string  `json:"source"`
	Profile               string  `json:"profile"`
}

type AttemptView struct {
	Number       int       `json:"number"`
	State        string    `json:"state"`
	MayDispatch  bool      `json:"may_dispatch"`
	ChannelID    uint      `json:"channel_id,omitempty"`
	CredentialID uint      `json:"credential_id,omitempty"`
	ActualModel  string    `json:"actual_model,omitempty"`
	Group        string    `json:"group,omitempty"`
	Protocol     string    `json:"protocol,omitempty"`
	Host         HostFacts `json:"host"`
	Usage        UsageView `json:"usage"`
}

type Detail struct {
	Record   Summary       `json:"record"`
	Attempts []AttemptView `json:"attempts"`
}

type Page struct {
	Data             []Summary `json:"data"`
	HasMore          bool      `json:"has_more"`
	Enabled          bool      `json:"enabled"`
	RetentionSeconds int       `json:"retention_seconds"`
}

type Repository interface {
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Detail, error)
}

type Service interface {
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Detail, error)
}
