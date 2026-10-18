package execution

import (
	"context"
	"time"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
)

const (
	NotDispatched                   = v1.NotDispatched
	Dispatching                     = v1.Dispatching
	ResponseReceived                = v1.ResponseReceived
	Success                         = v1.Success
	Canceled                        = v1.Canceled
	ClientWriteError                = v1.ClientWriteError
	Failed           v1.Termination = "failed"
)

type ConnectionID string
type RoundID uint64

type Frame struct {
	Type uint8
	Data []byte
}

// Connection is already authenticated and capacity-admitted by its owner.
// The execution layer only owns bounded round I/O and cancellation.
type Connection interface {
	ID() ConnectionID
	Read(context.Context) (Frame, error)
	Write(context.Context, Frame) (int, error)
	Close() error
}

type RoundHandler func(context.Context, Frame) ([]Frame, bool, error)

type Round struct {
	Connection Connection
	ID         RoundID
	Deadline   time.Time
	Handle     RoundHandler
}

type RoundResult struct {
	Dispatch      v1.Phase
	FramesRead    uint64
	FramesWritten uint64
	// BytesWritten is a confirmed payload-byte lower bound, not a wire count.
	BytesWritten uint64
	// Committed includes an attempted write whose delivery cannot be disproved.
	Committed        bool
	ProtocolComplete bool
	Termination      v1.Termination
}

type RoundExecutor interface {
	ExecuteRound(context.Context, Round) RoundResult
}

type Lease interface {
	Revision() string
	Release() error
}
