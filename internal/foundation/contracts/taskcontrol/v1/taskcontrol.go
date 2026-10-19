// Package taskcontrol exposes session-authorized metadata, never task content.
package taskcontrol

import (
	"context"
	"errors"

	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

const Version = "1"

var (
	ErrUnauthorized = errors.New("valid login session required")
	ErrForbidden    = errors.New("task scope is forbidden")
	ErrNotFound     = errors.New("background task not found")
	ErrInvalid      = errors.New("invalid task query")
	ErrConflict     = errors.New("task revision changed")
	ErrRateLimited  = errors.New("task console rate exceeded")
	ErrUnavailable  = errors.New("task console unavailable")
)

// Administrator requests a scope; the repository must verify the current role.
type Access struct {
	Session       string
	Administrator bool
}
type Query struct {
	Stage            string
	OwnerID, TokenID uint
	Limit, Offset    int
}
type Task struct {
	ProtocolStatus    string             `json:"protocol_status"`
	Usage             *history.UsageView `json:"usage"`
	ID                string             `json:"id"`
	UserID            uint               `json:"user_id"`
	TokenID           uint               `json:"token_id"`
	Model             string             `json:"model"`
	Stage             string             `json:"stage"`
	Revision          uint64             `json:"revision,string"`
	CreatedAt         int64              `json:"created_at"`
	Deadline          int64              `json:"deadline"`
	ContentExpiresAt  int64              `json:"content_expires_at"`
	MetadataExpiresAt int64              `json:"metadata_expires_at"`
	CancelRequested   bool               `json:"cancel_requested"`
	ContentAvailable  bool               `json:"content_available"`
	Deleted           bool               `json:"deleted"`
	ReplayPending     bool               `json:"replay_pending"`
	EventCount        int64              `json:"event_count"`
	LastSequence      *int64             `json:"last_sequence"`
	CanCancel         bool               `json:"can_cancel"`
}
type Page struct {
	Data    []Task `json:"data"`
	HasMore bool   `json:"has_more"`
}
type Repository interface {
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Task, error)
	Cancel(context.Context, Access, string, uint64) (Task, error)
}
type Service interface {
	List(context.Context, Access, Query) (Page, error)
	Read(context.Context, Access, string) (Task, error)
	Cancel(context.Context, Access, string, uint64) (Task, error)
}
