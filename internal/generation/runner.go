// Package generation provides bounded volatile and durable generation consumers.
// Runner is process-local; only IntentAcceptor plus DurableRunner persist work.
// Neither defines a public background API or a provider-native task protocol.
package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"minigateway/internal/access"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

var (
	ErrClosed               = errors.New("generation runner is closed")
	ErrFull                 = errors.New("generation queue is full")
	ErrAuthorityUnavailable = errors.New("generation identity authority is unavailable")
	ErrExecutionPanic       = errors.New("generation consumer panicked; execution may be unknown")
	ErrResultConsumed       = errors.New("generation result was already consumed")
)

type Options struct {
	Parser        execv1.Parser
	Workers       int
	QueueCapacity int
	// Configuration caps the combined queued/active body copies at 64 MiB.
	MaxRequestBytes int
	// MaxDuration cancels queue residence, authorization and execution together.
	// Coordinator finalization still runs before a worker releases ownership.
	MaxDuration time.Duration
	Resolve     func(context.Context, access.Binding) (access.Principal, error)
	Execute     func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome
	// Archive enables opt-in encrypted output recording, not durable job acceptance.
	Archive *Archive
}

type Runner struct {
	mu           sync.Mutex
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	jobs         chan *job
	pending      int
	preparations map[*submissionPreparation]struct{}
	submissions  sync.WaitGroup
	done         chan struct{}
	opts         Options
}

type submissionPreparation struct{ cancel context.CancelFunc }

type outcome struct {
	result execv1.Outcome
	err    error
}

// Future has one result consumer. A canceled Wait only stops observation;
// Cancel explicitly cancels the generation. Neither action proves zero cost.
type Future struct {
	cancel context.CancelFunc
	result chan outcome
}

func (f *Future) Cancel() { f.cancel() }

func (f *Future) Wait(ctx context.Context) (execv1.Outcome, error) {
	select {
	case <-ctx.Done():
		return execv1.Outcome{}, ctx.Err()
	case out, ok := <-f.result:
		if !ok {
			return execv1.Outcome{}, ErrResultConsumed
		}
		return out.result, out.err
	}
}

type job struct {
	ctx       context.Context
	binding   access.Binding
	requestID string
	output    execv1.Output
	in        execv1.Invocation
	future    *Future
}

func New(ctx context.Context, opts Options) (*Runner, error) {
	if ctx == nil || (opts.Archive != nil && nilPort(opts.Parser)) || opts.Resolve == nil || opts.Execute == nil || opts.Workers < 1 || opts.Workers > 1024 ||
		opts.QueueCapacity < 1 || opts.QueueCapacity > 4096 || opts.MaxRequestBytes < 1 || opts.MaxRequestBytes > 20<<20 ||
		int64(opts.Workers+opts.QueueCapacity)*int64(opts.MaxRequestBytes) > 64<<20 ||
		opts.MaxDuration <= 0 || opts.MaxDuration > 24*time.Hour ||
		(opts.Archive != nil && opts.MaxDuration+5*time.Second > opts.Archive.retention) {
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
		r.submissions.Wait()
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
func (r *Runner) Submit(observer context.Context, binding access.Binding, output execv1.Output, in execv1.Invocation) (*Future, error) {
	f, _, err := r.submit(observer, binding, output, in, false)
	return f, err
}

// SubmitRecorded reserves bounded output storage before volatile handoff. Only
// output is journaled: no request body, job intent, budget hold or restart replay.
func (r *Runner) SubmitRecorded(observer context.Context, binding access.Binding, in execv1.Invocation) (*Future, string, error) {
	if r.opts.Archive == nil {
		return nil, "", ErrArchiveUnavailable
	}
	return r.submit(observer, binding, nil, in, true)
}

func (r *Runner) submit(observer context.Context, binding access.Binding, output execv1.Output, in execv1.Invocation, recorded bool) (*Future, string, error) {
	if err := observer.Err(); err != nil {
		return nil, "", err
	}
	if binding.UserID == 0 || binding.TokenID == 0 {
		return nil, "", access.ErrUnauthorized
	}
	requestID := access.RequestID(observer)
	if (!recorded && output == nil) || (in.Operation != execv1.Chat && in.Operation != execv1.Embeddings) ||
		len(in.Body) == 0 || len(in.Body) > r.opts.MaxRequestBytes || len(in.SessionID) > 256 || len(requestID) > 256 {
		return nil, "", fmt.Errorf("invalid or oversized generation handoff")
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil, "", ErrClosed
	}
	if err := observer.Err(); err != nil {
		r.mu.Unlock()
		return nil, "", err
	}
	if len(r.jobs)+r.pending >= cap(r.jobs) {
		r.mu.Unlock()
		return nil, "", ErrFull
	}
	r.pending++
	r.submissions.Add(1)
	preparationContext, cancelPreparation := context.WithCancel(observer)
	preparation := &submissionPreparation{cancel: cancelPreparation}
	if r.preparations == nil {
		r.preparations = make(map[*submissionPreparation]struct{})
	}
	r.preparations[preparation] = struct{}{}
	stopPreparation := context.AfterFunc(r.ctx, cancelPreparation)
	r.mu.Unlock()
	pending := true
	defer func() {
		stopPreparation()
		cancelPreparation()
		r.mu.Lock()
		delete(r.preparations, preparation)
		if pending {
			r.pending--
		}
		r.mu.Unlock()
		r.submissions.Done()
	}()
	// Reserve queue capacity before storage I/O without holding the shutdown lock.
	// Close joins in-progress handoffs before allowing the store to be closed.
	in.Body = append([]byte(nil), in.Body...)
	in.SessionID = strings.Clone(in.SessionID)
	requestID = strings.Clone(requestID)
	id := ""
	var preparationErr error
	if recorded {
		request, err := r.opts.Parser.ParseRequest(in.Operation, in.Body)
		model := ""
		if err == nil && !nilPort(request) {
			model = request.Model()
		}
		if err != nil || nilPort(request) || len(model) > 256 {
			preparationErr = fmt.Errorf("invalid recorded generation request")
		} else {
			storageContext, cancelStorage := context.WithTimeout(preparationContext, 2*time.Second)
			out, err := r.opts.Archive.start(storageContext, binding)
			cancelStorage()
			if err != nil {
				preparationErr = err
			} else {
				out.model = model
				output, id = out, out.record.ID
			}
		}
	}
	r.mu.Lock()
	if preparationErr == nil {
		if r.closed || r.ctx.Err() != nil {
			preparationErr = ErrClosed
		} else {
			preparationErr = observer.Err()
		}
	}
	if preparationErr != nil {
		r.mu.Unlock()
		if out, ok := output.(*recordOutput); ok {
			_ = out.finish(execv1.Outcome{Operation: in.Operation, Result: execv1.Result{Phase: execv1.NotDispatched}}, context.Canceled)
		}
		return nil, id, preparationErr
	}
	defer r.mu.Unlock()
	r.pending--
	pending = false
	delete(r.preparations, preparation)
	ctx, cancel := context.WithTimeout(r.ctx, r.opts.MaxDuration)
	future := &Future{cancel: cancel, result: make(chan outcome, 1)}
	r.jobs <- &job{ctx: ctx, binding: binding, requestID: requestID, output: output, in: in, future: future}
	return future, id, nil
}

func (r *Runner) run(j *job) (out outcome) {
	out.result = execv1.Outcome{Operation: j.in.Operation,
		Identity: execv1.Identity{UserID: j.binding.UserID, TokenID: j.binding.TokenID, RequestID: j.requestID},
		Result:   execv1.Result{StatusCode: 503, Phase: execv1.NotDispatched}}
	defer func() {
		if recover() != nil {
			// Never replay a panicked generation: it may already have reached the
			// provider. Gateway owns its accounting and resource-unwind defers.
			out.err = ErrExecutionPanic
			out.result.StatusCode = 500
			out.result.Replayable = false
			if out.result.Phase != execv1.NotDispatched {
				state := panicOutputState(j.output)
				out.result.Committed, out.result.HTTPStatus = state.Committed, state.HTTPStatus
			}
		}
		if output, ok := j.output.(*recordOutput); ok {
			if err := output.finish(out.result, out.err); err != nil {
				out.err = errors.Join(out.err, ErrArchiveUnavailable)
				out.result.Replayable = false
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
	out.result.Phase = execv1.Dispatching
	out.result = r.opts.Execute(ctx, j.output, j.in)
	return out
}

func (out *outcome) contextError(err error) {
	out.err = err
	out.result.StatusCode, out.result.Termination = 499, execv1.Canceled
	if errors.Is(err, context.DeadlineExceeded) {
		out.result.StatusCode, out.result.Termination = 504, execv1.Deadline
	}
}

func panicOutputState(output execv1.Output) (state execv1.OutputState) {
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
	// Close owns cancellation ordering; AfterFunc alone may run after Close returns.
	for preparation := range r.preparations {
		preparation.cancel()
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
