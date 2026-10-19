package responsehistory

import (
	"context"
	"encoding/json"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

const MaxContextItems = 1024
const MaxContextBytes = 256 << 10

type StoredCreate struct {
	Model, PreviousID string
	JSON              json.RawMessage
}

type CompiledCreate struct {
	JSON       json.RawMessage
	Input      []json.RawMessage
	PromptText string
}

type StoredEvent struct {
	JSON     json.RawMessage
	Terminal bool
	Response json.RawMessage
	Output   []json.RawMessage
}

// StoredCodec operates on the declared text/function subset. Compile constructs
// full, self-contained input; Project receives an already validated event. Both
// are bounded, synchronous value transformations without I/O or policy.
type StoredCodec interface {
	Compile(json.RawMessage, []json.RawMessage, string) (CompiledCreate, error)
	Project(json.RawMessage, string, string, string) (StoredEvent, error)
}

type PreparedRound struct {
	ID, PromptText string
	JSON           json.RawMessage
	Target         *execution.TargetConstraint
}

// StoredRound has one serial execution owner. Observe persists a known terminal
// before returning its public frame. Finish must run once on every exit and
// conservatively retains unknown dispatched execution; it never resends.
type StoredRound interface {
	Prepared() PreparedRound
	Observe(context.Context, json.RawMessage, execution.TargetConstraint) (json.RawMessage, error)
	Finish(context.Context, bool) error
}

type StoredRounds interface {
	Prepare(context.Context, identity.Binding, StoredCreate) (StoredRound, error)
}
