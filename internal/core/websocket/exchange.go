package websocket

import (
	"context"
	"errors"
	"io"
	"time"
)

type Exchange struct {
	Upstream Connection
	Deadline time.Time
	Initial  Frame
	Decode   func(context.Context, Frame) ([]Frame, bool, error)
	Deliver  func(context.Context, Frame) (int, error)
}

type ExchangeResult struct {
	Dispatch             string
	UpstreamWriteStarted bool
	UpstreamBytesWritten uint64
	DeliveryWriteStarted bool
	DeliveryBytesWritten uint64
	ProtocolComplete     bool
	Termination          string
	Err                  error
}

// ExecuteExchange sends exactly once. Connection lifetime and retry policy
// belong to the caller, including when a failed write may have been observed.
func ExecuteExchange(parent context.Context, exchange Exchange) ExchangeResult {
	r := ExchangeResult{Dispatch: NotDispatched, Termination: Failed}
	if parent == nil || exchange.Upstream == nil || exchange.Decode == nil || exchange.Deliver == nil {
		r.Err = errors.New("invalid websocket exchange")
		return r
	}
	ctx := parent
	if !exchange.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(parent, exchange.Deadline)
		defer cancel()
	}
	fail := func(err error, fallback string) ExchangeResult {
		r.Err, r.Termination = err, fallback
		if termination(ctx, err) == Canceled {
			r.Termination = Canceled
		}
		return r
	}
	if ctx.Err() != nil {
		return fail(ctx.Err(), Canceled)
	}
	r.Dispatch, r.UpstreamWriteStarted = Dispatching, true
	n, err := exchange.Upstream.Write(ctx, exchange.Initial)
	r.UpstreamBytesWritten, err = exchangeWriteCount(n, len(exchange.Initial.Data), err)
	if err != nil {
		return fail(err, Failed)
	}
	for {
		if ctx.Err() != nil {
			return fail(ctx.Err(), Canceled)
		}
		frame, err := exchange.Upstream.Read(ctx)
		if err != nil {
			return fail(err, Failed)
		}
		r.Dispatch = ResponseReceived
		if ctx.Err() != nil {
			return fail(ctx.Err(), Canceled)
		}
		frames, complete, err := exchange.Decode(ctx, frame)
		if err != nil {
			return fail(err, Failed)
		}
		r.ProtocolComplete = complete
		for _, output := range frames {
			if ctx.Err() != nil {
				return fail(ctx.Err(), Canceled)
			}
			r.DeliveryWriteStarted = true
			n, err := exchange.Deliver(ctx, output)
			count, err := exchangeWriteCount(n, len(output.Data), err)
			r.DeliveryBytesWritten += count
			if err != nil {
				return fail(err, ClientWriteError)
			}
		}
		if ctx.Err() != nil {
			return fail(ctx.Err(), Canceled)
		}
		if complete {
			r.Termination = Success
			return r
		}
	}
}

func exchangeWriteCount(n, size int, err error) (uint64, error) {
	if n < 0 || n > size {
		return 0, errors.New("invalid websocket write count")
	}
	if err == nil && n != size {
		err = io.ErrShortWrite
	}
	return uint64(n), err
}
