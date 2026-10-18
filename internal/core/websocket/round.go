// Package websocket provides bounded, protocol-neutral round I/O.
package websocket

import (
	"context"
	"errors"
	"time"
)

const Version = "1"

const (
	NotDispatched    = "not_dispatched"
	Dispatching      = "dispatching"
	ResponseReceived = "response_received"
	Success          = "success"
	Canceled         = "canceled"
	ClientWriteError = "client_write_error"
	Failed           = "failed"
)

type Frame struct {
	Type uint8
	Data []byte
}

type Connection interface {
	Read(context.Context) (Frame, error)
	Write(context.Context, Frame) (int, error)
}

type Round struct {
	Connection Connection
	Deadline   time.Time
	Handle     func(context.Context, Frame) ([]Frame, bool, error)
}

type Result struct {
	Dispatch         string
	FramesRead       uint64
	FramesWritten    uint64
	BytesWritten     uint64
	Committed        bool
	ProtocolComplete bool
	Termination      string
}

func Execute(parent context.Context, round Round) Result {
	result := Result{Dispatch: NotDispatched, Termination: Failed}
	if round.Connection == nil || round.Handle == nil {
		return result
	}
	ctx := parent
	var cancel context.CancelFunc
	if !round.Deadline.IsZero() {
		ctx, cancel = context.WithDeadline(parent, round.Deadline)
		defer cancel()
	}
	for {
		frame, err := round.Connection.Read(ctx)
		if err != nil {
			result.Termination = termination(ctx, err)
			return result
		}
		result.FramesRead++
		if result.Dispatch == NotDispatched {
			result.Dispatch = Dispatching
		}
		frames, complete, err := round.Handle(ctx, frame)
		if err != nil {
			result.Termination = termination(ctx, err)
			return result
		}
		for _, output := range frames {
			written, err := round.Connection.Write(ctx, output)
			if written > 0 {
				result.Committed = true
				result.BytesWritten += uint64(written)
			}
			if err != nil {
				result.Termination = ClientWriteError
				return result
			}
			result.FramesWritten++
		}
		if complete {
			result.Dispatch = ResponseReceived
			result.ProtocolComplete = true
			result.Termination = Success
			return result
		}
	}
}

func termination(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return Canceled
	}
	return Failed
}
