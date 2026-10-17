// Package generation provides bounded, process-local generation handoff. It is
// not a durable task queue and must not back a public background-acceptance API.
package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol"
)

var (
	ErrClosed               = errors.New("generation runner is closed")
	ErrFull                 = errors.New("generation queue is full")
	ErrAuthorityUnavailable = errors.New("generation identity authority is unavailable")
	ErrExecutionPanic       = errors.New("generation consumer panicked; execution may be unknown")
	ErrResultConsumed       = errors.New("generation result was already consumed")
)

type Options struct {
	Workers       int
	QueueCapacity int
	// Configuration caps the combined queued/active body copies at 64 MiB.
	MaxRequestBytes int
	// MaxDuration cancels queue residence, authorization and execution together.
	// Coordinator finalization still runs before a worker releases ownership.
	MaxDuration time.Duration
	Resolve     func(context.Context, access.Binding) (access.Principal, error)
	Execute     func(context.Context, core.Output, gateway.Execution) gateway.RequestResult
}

type Runner struct {
	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan *job
	done   chan struct{}
	opts   Options
}

type outcome struct {
	result gateway.RequestResult
	err    error
}

// Future has one result consumer. A canceled Wait only stops observation;
// Cancel explicitly cancels the generation. Neither action proves zero cost.
type Future struct {
	cancel context.CancelFunc
	result chan outcome
}

func (f *Future) Cancel() { f.cancel() }

func (f *Future) Wait(ctx context.Context) (gateway.RequestResult, error) {
	select {
	case <-ctx.Done():
		return gateway.RequestResult{}, ctx.Err()
	case out, ok := <-f.result:
		if !ok {
			return gateway.RequestResult{}, ErrResultConsumed
		}
		return out.result, out.err
	}
}

type job struct {
	ctx       context.Context
	binding   access.Binding
	requestID string
	output    core.Output
	in        gateway.Execution
	future    *Future
}

func New(ctx context.Context, opts Options) (*Runner, error) {
	if ctx == nil || opts.Resolve == nil || opts.Execute == nil || opts.Workers < 1 || opts.Workers > 1024 ||
		opts.QueueCapacity < 1 || opts.QueueCapacity > 4096 || opts.MaxRequestBytes < 1 || opts.MaxRequestBytes > 20<<20 ||
		int64(opts.Workers+opts.QueueCapacity)*int64(opts.MaxRequestBytes) > 64<<20 ||
		opts.MaxDuration <= 0 || opts.MaxDuration > 24*time.Hour {
		return nil, fmt.Errorf("invalid generation runner configuration")
	}
	root, cancel := context.WithCancel(ctx)
	r := &Runner{ctx: root, cancel: cancel, jobs: make(chan *job, opts.QueueCapacity), done: make(chan struct{}), opts: opts}
	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := range r.jobs {
				out := r.run(j)
				j.future.cancel()
				j.future.result <- out
				close(j.future.result)
			}
		}()
	}
	go func() {
		<-root.Done()
		r.stop()
		workers.Wait()
		close(r.done)
	}()
	return r, nil
}

// Submit copies only bounded execution data and a trusted binding. It never
// retains the observer context, its Principal, or its admission/reservation state.
// A successful handoff is volatile, not durable acceptance or a budget reserve.
// The caller transfers exclusive output ownership until the result is received
// or Close finishes, not merely until an observation is canceled.
func (r *Runner) Submit(observer context.Context, binding access.Binding, output core.Output, in gateway.Execution) (*Future, error) {
	if err := observer.Err(); err != nil {
		return nil, err
	}
	if binding.UserID == 0 || binding.TokenID == 0 {
		return nil, access.ErrUnauthorized
	}
	requestID := access.RequestID(observer)
	if output == nil || (in.Operation != protocol.Chat && in.Operation != protocol.Embeddings) ||
		len(in.Body) == 0 || len(in.Body) > r.opts.MaxRequestBytes || len(in.SessionID) > 256 || len(requestID) > 256 {
		return nil, fmt.Errorf("invalid or oversized generation handoff")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if err := observer.Err(); err != nil {
		return nil, err
	}
	if len(r.jobs) == cap(r.jobs) {
		return nil, ErrFull
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.opts.MaxDuration)
	future := &Future{cancel: cancel, result: make(chan outcome, 1)}
	in.Body = append([]byte(nil), in.Body...)
	in.SessionID = strings.Clone(in.SessionID)
	r.jobs <- &job{ctx: ctx, binding: binding, requestID: strings.Clone(requestID), output: output, in: in, future: future}
	return future, nil
}

func (r *Runner) run(j *job) (out outcome) {
	out.result = gateway.RequestResult{Operation: j.in.Operation,
		Identity: gateway.Identity{UserID: j.binding.UserID, TokenID: j.binding.TokenID, RequestID: j.requestID},
		Result:   core.Result{StatusCode: 503, Phase: core.NotDispatched}}
	defer func() {
		if recover() != nil {
			// Never replay a panicked generation: it may already have reached the
			// provider. Gateway owns its accounting and resource-unwind defers.
			out.err = ErrExecutionPanic
			out.result.StatusCode = 500
			out.result.Replayable = false
			if out.result.Phase != core.NotDispatched {
				state := panicOutputState(j.output)
				out.result.Committed, out.result.HTTPStatus = state.Committed, state.HTTPStatus
			}
		}
	}()
	if err := j.ctx.Err(); err != nil {
		out.contextError(err)
		return out
	}
	principal, err := r.opts.Resolve(j.ctx, j.binding)
	if err != nil {
		switch {
		case j.ctx.Err() != nil:
			out.contextError(j.ctx.Err())
		case errors.Is(err, access.ErrUnauthorized):
			out.result.StatusCode, out.err = 401, access.ErrUnauthorized
		default:
			out.err = ErrAuthorityUnavailable
		}
		return out
	}
	if principal.UserID != j.binding.UserID || principal.TokenID != j.binding.TokenID || principal.Policy.Validate() != nil ||
		principal.RPMLimit < 0 || principal.TPMLimit < 0 || principal.UserRPMLimit < 0 || principal.UserConcurrencyLimit < 0 || principal.KeyConcurrencyLimit < 0 {
		out.err = ErrAuthorityUnavailable
		return out
	}
	if err := j.ctx.Err(); err != nil {
		out.contextError(err)
		return out
	}
	ctx := access.WithPrincipal(j.ctx, principal)
	ctx = access.WithRequestID(ctx, j.requestID)
	// Conservative until Execute returns its own phase/commit facts.
	out.result.Phase = core.Dispatching
	out.result = r.opts.Execute(ctx, j.output, j.in)
	return out
}

func (out *outcome) contextError(err error) {
	out.err = err
	out.result.StatusCode, out.result.Termination = 499, core.Canceled
	if errors.Is(err, context.DeadlineExceeded) {
		out.result.StatusCode, out.result.Termination = 504, core.Deadline
	}
}

func panicOutputState(output core.Output) (state core.OutputState) {
	defer func() {
		if recover() != nil {
			state.Committed = true
		}
	}()
	return output.State()
}

func (r *Runner) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.jobs)
		r.cancel()
	}
}

// Close stops dispatch, cancels queued/active work and waits for finalization.
// On timeout the owner must keep gateway/store alive and wait again; trusted
// in-process callbacks that ignore cancellation cannot be forcibly terminated.
func (r *Runner) Close(ctx context.Context) error {
	r.stop()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
