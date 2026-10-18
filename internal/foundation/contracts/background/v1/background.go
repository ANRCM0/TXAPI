// Package background defines opt-in native asynchronous execution capabilities.
package background

import (
	"context"
	"errors"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

const Version = "1"

var (
	ErrUnavailable           = errors.New("background service unavailable")
	ErrUnauthorized          = errors.New("background identity unavailable")
	ErrNotFound              = errors.New("background response not found")
	ErrConflict              = errors.New("background request conflicts with durable state")
	ErrCapacity              = errors.New("background capacity exceeded")
	ErrUncertain             = errors.New("background execution may be unknown")
	ErrCursorUnavailable     = errors.New("background events unavailable")
	ErrUnsupportedBilling    = errors.New("background requires disabled billing")
	ErrUnsupportedCapability = errors.New("native background capability unavailable")
)

type Stage string

const (
	StageQueued       Stage = "queued"
	StageDispatching  Stage = "dispatching"
	StageRemoteActive Stage = "remote_active"
	StageTerminal     Stage = "terminal"
	StageUncertain    Stage = "uncertain"
)

type Submit struct {
	ID             string
	Operation      execution.Operation
	Body           []byte
	IdempotencyKey string
	Stream         bool
	Store          bool
	Deadline       time.Time
}
type Receipt struct {
	ID               string
	Revision         uint64
	Stage            Stage
	Model            string
	Document         []byte
	CancelRequested  bool
	CreatedAt        time.Time
	ContentExpiresAt time.Time
}
type EventQuery struct {
	After *uint64
	Limit int
}
type Event struct {
	Sequence uint64
	Data     []byte
}
type EventPage struct {
	Events   []Event
	Next     *uint64
	Terminal bool
}
type Service interface {
	Submit(context.Context, Submit) (Receipt, error)
	Read(context.Context, string) (Receipt, error)
	Cancel(context.Context, string) (Receipt, error)
	Events(context.Context, string, EventQuery) (EventPage, error)
}

type RemoteAction string

const (
	RemoteSubmit  RemoteAction = "submit"
	RemoteInspect RemoteAction = "inspect"
	RemoteCancel  RemoteAction = "cancel"
	RemoteResume  RemoteAction = "resume"
)

type RemoteCommand struct {
	Action   RemoteAction
	Body     []byte
	RemoteID string
	After    *uint64
}
type RemoteState string

const (
	RemoteAccepted   RemoteState = "accepted"
	RemoteRunning    RemoteState = "running"
	RemoteSucceeded  RemoteState = "succeeded"
	RemoteIncomplete RemoteState = "incomplete"
	RemoteFailed     RemoteState = "failed"
	RemoteCancelled  RemoteState = "cancelled"
)

type Observation struct {
	RemoteID string
	State    RemoteState
	Document []byte
	Events   []Event
	Usage    *execution.Usage
}
type Observer interface{ Drain() Observation }
type Prepared struct {
	Input    execution.Input
	Progress Observer
}
type Codec interface {
	Prepare(RemoteCommand, execution.Target) (Prepared, *execution.Error)
}
type Registration struct {
	ID        string
	Version   string
	Operation execution.Operation
	Parse     execution.Parser
	Codec     Codec
}
