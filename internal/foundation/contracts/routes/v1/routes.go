// Package routes exposes ephemeral routing observations, never execution authority.
package routes

import (
	"context"
	"errors"
)

const Version = "1"

var (
	ErrUnauthorized = errors.New("valid login session required")
	ErrForbidden    = errors.New("routing scope is forbidden")
	ErrInvalid      = errors.New("invalid routing query")
	ErrNotFound     = errors.New("routing observation not retained")
	ErrRateLimited  = errors.New("routing console rate exceeded")
	ErrUnavailable  = errors.New("routing console unavailable")
)

// Administrator requests a scope; Service must verify the current session role.
type Access struct {
	Session       string
	Administrator bool
}

// Scope is an injected read capability, not HTTP authentication proof.
type Scope struct {
	UserID        uint
	Administrator bool
}

type Query struct {
	RequestID        string
	OwnerID, TokenID uint
	Limit, Offset    int
}

type Selection struct {
	Outcome                string `json:"outcome"`
	Picker                 string `json:"picker"`
	CandidateChannels      int    `json:"candidate_channels"`
	EligibleChannels       int    `json:"eligible_channels"`
	SelectionPoolChannels  int    `json:"selection_pool_channels"`
	TotalCredentials       int    `json:"total_credentials"`
	TriedCredentials       int    `json:"tried_credentials"`
	UnavailableCredentials int    `json:"unavailable_credentials"`
	EligibleCredentials    int    `json:"eligible_credentials"`
	PriorityFallback       bool   `json:"priority_fallback"`
	DurationMicros         int64  `json:"duration_micros"`
	ChannelID              uint   `json:"channel_id,omitempty"`
	CredentialID           uint   `json:"credential_id,omitempty"`
}

type Attempt struct {
	Number           int    `json:"number"`
	Status           int    `json:"status"`
	HTTPStatus       int    `json:"http_status"`
	Phase            string `json:"phase"`
	Termination      string `json:"termination"`
	Committed        bool   `json:"committed"`
	ProtocolComplete bool   `json:"protocol_complete"`
	ChannelID        uint   `json:"channel_id,omitempty"`
	CredentialID     uint   `json:"credential_id,omitempty"`
	ActualModel      string `json:"actual_model,omitempty"`
}

type Trace struct {
	ID               string      `json:"id"`
	RequestID        string      `json:"request_id"`
	UserID           uint        `json:"user_id"`
	TokenID          uint        `json:"token_id"`
	Operation        string      `json:"operation"`
	Model            string      `json:"model"`
	StartedAt        int64       `json:"started_at"`
	FinishedAt       int64       `json:"finished_at"`
	Status           int         `json:"status"`
	HTTPStatus       int         `json:"http_status"`
	Reason           string      `json:"reason"`
	Phase            string      `json:"phase"`
	Termination      string      `json:"termination"`
	Committed        bool        `json:"committed"`
	ProtocolComplete bool        `json:"protocol_complete"`
	Interrupted      bool        `json:"interrupted"`
	Truncated        bool        `json:"truncated"`
	Selections       []Selection `json:"selections"`
	Attempts         []Attempt   `json:"attempts"`
}

type Page struct {
	Data             []Trace `json:"data"`
	HasMore          bool    `json:"has_more"`
	Enabled          bool    `json:"enabled"`
	Capacity         int     `json:"capacity"`
	RetentionSeconds int     `json:"retention_seconds"`
}

type Reader interface {
	List(context.Context, Scope, Query) (Page, error)
	Read(context.Context, Scope, string) (Trace, error)
}

type Service interface {
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Trace, error)
}
