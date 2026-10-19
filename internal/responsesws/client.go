// Package responsesws coordinates a bounded downstream client with the native host.
package responsesws

import (
	"context"
	"sync/atomic"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
)

type client struct {
	idle     time.Duration
	terminal func(execution.Frame) bool
}

func NewClient(idle time.Duration) contract.Client { return &client{idle: idle} }

// NewClientWithTerminal accepts a presentation-owned classifier for delivered frames.
func NewClientWithTerminal(idle time.Duration, terminal func(execution.Frame) bool) contract.Client {
	return &client{idle: idle, terminal: terminal}
}

const (
	clientIdle int32 = iota
	clientRunning
	clientFinishing
	clientPending
)

// Run owns one reader and one synchronous worker, with at most one boundary frame.
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
	var phase atomic.Int32
	reserve := func() bool {
		for {
			switch current := phase.Load(); current {
			case clientIdle:
				if phase.CompareAndSwap(current, clientRunning) {
					return true
				}
			case clientFinishing:
				if phase.CompareAndSwap(current, clientPending) {
					return true
				}
			default:
				return false
			}
		}
	}
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
			if !reserve() {
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
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-session.Done():
				p := session.Problem()
				if p == nil {
					p = &contract.Problem{Code: "session_expired", CloseCode: 1000}
				}
				report(*p)
				return p
			default:
			}
			phase.CompareAndSwap(clientPending, clientRunning)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			deliveryFailed := false
			result := session.Execute(ctx, contract.Create{JSON: frame.Data}, func(writeContext context.Context, output execution.Frame) (int, error) {
				if c.terminal != nil && c.terminal(output) {
					// A peer can observe the terminal frame before Write returns.
					phase.CompareAndSwap(clientRunning, clientFinishing)
				}
				n, err := connection.Write(writeContext, output)
				deliveryFailed = deliveryFailed || err != nil || n != len(output.Data)
				return n, err
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
			if ctx.Err() != nil || deliveryFailed {
				return nil
			}
			// Preserve a reader's reserved boundary frame until this worker takes it.
			phase.CompareAndSwap(clientRunning, clientIdle)
			phase.CompareAndSwap(clientFinishing, clientIdle)
			idle.Reset(c.idle)
		}
	}
}

var _ contract.Client = (*client)(nil)
