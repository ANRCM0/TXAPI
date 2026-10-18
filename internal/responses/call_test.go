package responses

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	application "minigateway/internal/foundation/contracts/responses/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

type callAuthority struct{}

func (callAuthority) Current(context.Context) (identity.Principal, bool) {
	return identity.Principal{UserID: 1, TokenID: 2, Policy: identity.Policy{Mode: identity.All}}, true
}

func (a callAuthority) Refresh(ctx context.Context, _ identity.Binding) (context.Context, identity.Principal, error) {
	principal, _ := a.Current(ctx)
	return ctx, principal, nil
}

type callReceipts struct{ finished atomic.Int32 }

func (*callReceipts) InitializeResponseRequests(context.Context, string) error { return nil }
func (*callReceipts) BeginResponseRequest(_ context.Context, in state.ResponseRequest, _ state.ResponseRequestLimits) (state.ResponseRequest, bool, error) {
	return in, true, nil
}
func (r *callReceipts) FinishResponseRequest(context.Context, state.ResponseRequest, state.ResponseRequestCompletion) error {
	r.finished.Add(1)
	return nil
}

type callArchive struct{ state.Archive }
type callExecutor struct{ calls atomic.Int32 }

func (e *callExecutor) Execute(ctx context.Context, out execution.Output, in execution.Invocation) execution.Outcome {
	e.calls.Add(1)
	_ = out.Commit(ctx, execution.ResponseMetadata{StatusCode: 200})
	_, _ = out.Write(ctx, []byte(`{"object":"response"}`))
	return execution.Outcome{Operation: execution.Responses, Native: execution.NativeCompletion{Status: "completed"}, Result: execution.Result{StatusCode: 200, Phase: execution.ResponseReceived, ProtocolComplete: true, Committed: true, Termination: execution.Success}}
}

type callDelivery struct {
	state   execution.OutputState
	problem application.Problem
}

func (d *callDelivery) Commit(context.Context, execution.ResponseMetadata) error {
	d.state.Committed = true
	return nil
}
func (*callDelivery) Write(_ context.Context, p []byte) (int, error) { return len(p), nil }
func (*callDelivery) Flush(context.Context) error                    { return nil }
func (d *callDelivery) State() execution.OutputState                 { return d.state }
func (*callDelivery) SetWriteDeadline(time.Time) error               { return nil }
func (*callDelivery) Annotate(application.Metadata)                  {}
func (d *callDelivery) Problem(p application.Problem)                { d.problem = p }
func (*callDelivery) Document(application.Document)                  {}

type callPayload struct{ entered, release chan struct{} }

func (p callPayload) Read() ([]byte, error) {
	close(p.entered)
	<-p.release
	return []byte(`{"model":"m","input":"hello","store":false}`), nil
}

func TestCallCloseJoinsOwnedWorkAndCannotReuseAuthorizationOrCapacity(t *testing.T) {
	receipts, executor := &callReceipts{}, &callExecutor{}
	service, err := New(context.Background(), Options{Store: receipts, Executor: executor, Archive: &callArchive{}, Identity: callAuthority{}, DigestKey: bytes.Repeat([]byte{1}, 32), Retention: time.Hour, Limits: state.ResponseRequestLimits{InstanceRecords: 4, OwnerRecords: 2}, ActiveRequests: 2, OwnerRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	call, failure := service.Begin(context.Background(), application.CreateResponse)
	if failure != nil {
		t.Fatal(failure)
	}
	defer call.Close()
	entered, release, handled, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	go func() {
		defer close(handled)
		call.Handle(application.Command{Action: application.CreateResponse, Payload: callPayload{entered: entered, release: release}}, &callDelivery{})
	}()
	<-entered
	go func() { call.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close released the owner while its payload callback still ran")
	case <-time.After(20 * time.Millisecond):
	}
	if next, failure := service.Begin(context.Background(), application.CreateResponse); failure == nil || failure.Code != "rate_limit_error" {
		if next != nil {
			next.Close()
		}
		t.Fatal("active owner capacity was released before work completed")
	}
	unblock.Do(func() { close(release) })
	<-handled
	<-closed
	call.Close()
	reused := &callDelivery{}
	call.Handle(application.Command{Action: application.CreateResponse}, reused)
	if reused.problem.Code != "request_closed" || executor.calls.Load() != 1 || receipts.finished.Load() != 1 {
		t.Fatal("a completed call reused its authorization or execution")
	}
	if next, failure := service.Begin(context.Background(), application.CreateResponse); failure != nil {
		t.Fatal("joined work did not release capacity")
	} else {
		next.Close()
	}
}

type panicOnceAuthority struct {
	callAuthority
	refreshed atomic.Bool
}

func (a *panicOnceAuthority) Refresh(ctx context.Context, binding identity.Binding) (context.Context, identity.Principal, error) {
	if !a.refreshed.Swap(true) {
		panic("isolated identity callback failure")
	}
	return a.callAuthority.Refresh(ctx, binding)
}

func TestBeginPanicReturnsCapacityBeforeAnotherCall(t *testing.T) {
	service, err := New(context.Background(), Options{Store: &callReceipts{}, Executor: &callExecutor{}, Archive: &callArchive{}, Identity: &panicOnceAuthority{}, DigestKey: bytes.Repeat([]byte{1}, 32), Retention: time.Hour, Limits: state.ResponseRequestLimits{InstanceRecords: 4, OwnerRecords: 2}, ActiveRequests: 2, OwnerRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _ = service.Begin(context.Background(), application.CreateResponse)
	}()
	if !panicked {
		t.Fatal("fixture did not reach the interrupted authority callback")
	}
	call, failure := service.Begin(context.Background(), application.CreateResponse)
	if failure != nil {
		t.Fatalf("interrupted Begin leaked owner capacity: %+v", failure)
	}
	call.Close()
}
