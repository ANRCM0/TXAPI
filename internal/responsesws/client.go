// Package responsesws coordinates a bounded downstream client with the native host.
package responsesws

import (
	"context"
	"sync/atomic"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
)

type client struct{ idle time.Duration }

func NewClient(idle time.Duration) contract.Client { return &client{idle: idle} }

// Run owns one reader and one synchronous worker, with no pending generation.
// Every reported problem goes through notify exactly once; the return value is
// for the adapter's internal observation, not a second wire delivery.
func (c *client) Run(parent context.Context, session contract.Session, connection execution.Connection, notify contract.ProblemDelivery) *contract.Problem {
	if parent == nil || session == nil || connection == nil || notify == nil || c.idle <= 0 {
		return &contract.Problem{Code: "unavailable", CloseCode: 1013}
	}
	ctx, cancel := context.WithCancel(parent)
	requests := make(chan execution.Frame, 1)
	fatal := make(chan *contract.Problem, 1)
	readerDone := make(chan struct{})
	var active atomic.Bool
	report := func(p contract.Problem) {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		_ = notify(cleanup, p)
	}
	go func() {
		defer close(readerDone)
		for {
			frame, err := connection.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			if frame.Type != 1 {
				p := &contract.Problem{Code: "unsupported_frame", CloseCode: 1003}
				report(*p)
				fatal <- p
				cancel()
				return
			}
			if !active.CompareAndSwap(false, true) {
				if err := notify(ctx, contract.Problem{Code: "busy"}); err != nil {
					cancel()
					return
				}
				continue
			}
			select {
			case requests <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = connection.Close(); <-readerDone }()
	idle := time.NewTimer(c.idle)
	defer idle.Stop()
	for {
		select {
		case <-session.Done():
			p := session.Problem()
			if p == nil {
				p = &contract.Problem{Code: "session_expired", CloseCode: 1000}
			}
			report(*p)
			return p
		case <-ctx.Done():
			select {
			case p := <-fatal:
				return p
			default:
				return nil
			}
		case <-idle.C:
			p := &contract.Problem{Code: "session_expired", CloseCode: 1000}
			report(*p)
			return p
		case frame := <-requests:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			result := session.Execute(ctx, contract.Create{JSON: frame.Data}, func(writeContext context.Context, output execution.Frame) (int, error) {
				return connection.Write(writeContext, output)
			})
			select {
			case p := <-fatal:
				return p
			default:
			}
			if result.Problem != nil {
				report(*result.Problem)
				return result.Problem
			}
			if ctx.Err() != nil {
				return nil
			}
			active.Store(false)
			idle.Reset(c.idle)
		}
	}
}

var _ contract.Client = (*client)(nil)
