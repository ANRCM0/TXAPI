package responsesws

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
)

type boundarySignal struct {
	done chan struct{}
	once sync.Once
}

func newBoundarySignal() boundarySignal { return boundarySignal{done: make(chan struct{})} }
func (s *boundarySignal) close()        { s.once.Do(func() { close(s.done) }) }

type boundaryConnection struct {
	input        chan execution.Frame
	readEntered  chan int32
	reads        atomic.Int32
	reading      atomic.Int32
	maxReading   atomic.Int32
	closed       boundarySignal
	writeStarted boundarySignal
	writeRelease boundarySignal
	writeFailed  bool
	writeShort   bool
}

func (*boundaryConnection) ID() execution.ConnectionID { return "terminal-boundary" }
func boundaryMaximum(maximum *atomic.Int32, value int32) {
	for previous := maximum.Load(); value > previous; previous = maximum.Load() {
		if maximum.CompareAndSwap(previous, value) {
			return
		}
	}
}
func (c *boundaryConnection) Read(ctx context.Context) (execution.Frame, error) {
	boundaryMaximum(&c.maxReading, c.reading.Add(1))
	defer c.reading.Add(-1)
	c.readEntered <- c.reads.Add(1)
	select {
	case frame := <-c.input:
		return frame, nil
	case <-ctx.Done():
		return execution.Frame{}, ctx.Err()
	case <-c.closed.done:
		return execution.Frame{}, errors.New("closed")
	}
}
func (c *boundaryConnection) Write(ctx context.Context, frame execution.Frame) (int, error) {
	c.writeStarted.close()
	select {
	case <-c.writeRelease.done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if c.writeFailed {
		return 0, errors.New("terminal write failed")
	}
	if c.writeShort {
		return len(frame.Data) - 1, nil
	}
	return len(frame.Data), nil
}
func (c *boundaryConnection) Close() error { c.closed.close(); return nil }

type boundarySession struct {
	done      boundarySignal
	cleanup   boundarySignal
	delivered boundarySignal
	output    execution.Frame
	failure   *contract.Problem
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}

func (s *boundarySession) Execute(ctx context.Context, _ contract.Create, delivery execution.FrameDelivery) contract.Result {
	boundaryMaximum(&s.maxActive, s.active.Add(1))
	defer s.active.Add(-1)
	if s.calls.Add(1) > 1 {
		return contract.Result{Problem: &contract.Problem{Code: "continuation_invalid"}}
	}
	_, _ = delivery(ctx, s.output)
	s.delivered.close()
	select {
	case <-s.cleanup.done:
	case <-ctx.Done():
	}
	return contract.Result{Problem: s.failure}
}
func (s *boundarySession) Done() <-chan struct{}       { return s.done.done }
func (*boundarySession) Problem() *contract.Problem    { return nil }
func (s *boundarySession) Close(context.Context) error { s.done.close(); return nil }

type boundaryHarness struct {
	ctx           context.Context
	cancel        context.CancelFunc
	connection    *boundaryConnection
	session       *boundarySession
	notifications chan string
	result        chan *contract.Problem
	joined        boundarySignal
}

func newBoundaryHarness(t *testing.T, client contract.Client, blockedWrite bool, failure *contract.Problem, writeFailed bool, terminal bool) *boundaryHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	connection := &boundaryConnection{input: make(chan execution.Frame), readEntered: make(chan int32, 8),
		closed: newBoundarySignal(), writeStarted: newBoundarySignal(), writeRelease: newBoundarySignal(), writeFailed: writeFailed}
	if !blockedWrite {
		connection.writeRelease.close()
	}
	output := "terminal"
	if !terminal {
		output = "delta"
	}
	session := &boundarySession{done: newBoundarySignal(), cleanup: newBoundarySignal(), delivered: newBoundarySignal(),
		output: execution.Frame{Type: 1, Data: []byte(output)}, failure: failure}
	h := &boundaryHarness{ctx: ctx, cancel: cancel, connection: connection, session: session,
		notifications: make(chan string, 8), result: make(chan *contract.Problem, 1), joined: newBoundarySignal()}
	go func() {
		defer h.joined.close()
		h.result <- client.Run(ctx, session, connection, func(_ context.Context, problem contract.Problem) error { h.notifications <- problem.Code; return nil })
	}()
	t.Cleanup(func() {
		cancel()
		connection.writeRelease.close()
		session.cleanup.close()
		select {
		case <-h.joined.done:
		case <-time.After(5 * time.Second):
			t.Error("client did not join its bounded reader/worker")
		}
	})
	return h
}

func boundaryPredicate(frame execution.Frame) bool {
	return frame.Type == 1 && bytes.Equal(frame.Data, []byte("terminal"))
}
func (h *boundaryHarness) send(t *testing.T, data string, nextRead int32) {
	t.Helper()
	select {
	case h.connection.input <- execution.Frame{Type: 1, Data: []byte(data)}:
	case <-h.ctx.Done():
		t.Fatal("client did not read input", h.ctx.Err())
	}
	for {
		select {
		case count := <-h.connection.readEntered:
			if count >= nextRead {
				return
			}
		case <-h.ctx.Done():
			t.Fatal("reader did not process frame", h.ctx.Err())
		}
	}
}
func (h *boundaryHarness) wait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-h.ctx.Done():
		t.Fatal("boundary synchronization failed", h.ctx.Err())
	}
}
func (h *boundaryHarness) finish(t *testing.T) *contract.Problem {
	t.Helper()
	h.wait(t, h.joined.done)
	return <-h.result
}
func (h *boundaryHarness) expectNoProblem(t *testing.T) {
	t.Helper()
	select {
	case problem := <-h.notifications:
		t.Fatal("boundary request was rejected", problem)
	default:
	}
	if h.session.calls.Load() != 1 || h.session.maxActive.Load() != 1 {
		t.Fatal("pending frame executed before prior cleanup", h.session.calls.Load(), h.session.maxActive.Load())
	}
}

func TestClientTerminalBoundaryWaitsForCleanupAndBoundsPendingFrame(t *testing.T) {
	for _, blockedWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "terminal-write"}[blockedWrite], func(t *testing.T) {
			h := newBoundaryHarness(t, NewClientWithTerminal(time.Second, boundaryPredicate), blockedWrite, nil, false, true)
			h.send(t, "first", 2)
			h.wait(t, h.connection.writeStarted.done)
			h.send(t, "next", 3)
			h.expectNoProblem(t)
			h.send(t, "excess", 4)
			select {
			case code := <-h.notifications:
				if code != "busy" {
					t.Fatal(code)
				}
			default:
				t.Fatal("second pending frame was not rejected")
			}
			h.connection.writeRelease.close()
			h.wait(t, h.session.delivered.done)
			h.expectNoProblem(t)
			h.session.cleanup.close()
			if problem := h.finish(t); problem == nil || problem.Code != "continuation_invalid" {
				t.Fatal("accepted boundary frame did not reach continuation checks", problem)
			}
			if h.session.calls.Load() != 2 || h.session.maxActive.Load() != 1 || h.connection.maxReading.Load() != 1 {
				t.Fatal("client overlapped execution or readers", h.session.calls.Load(), h.session.maxActive.Load(), h.connection.maxReading.Load())
			}
			select {
			case code := <-h.notifications:
				if code != "continuation_invalid" {
					t.Fatal(code)
				}
			default:
				t.Fatal("continuation problem missing")
			}
			select {
			case code := <-h.notifications:
				t.Fatal("problem delivered twice", code)
			default:
			}
		})
	}
}

func TestClientTerminalBoundaryDiscardsPendingOnFailureOrCancellation(t *testing.T) {
	for _, kind := range []string{"write-failure", "short-write", "cleanup-failure", "parent-cancel", "session-done"} {
		t.Run(kind, func(t *testing.T) {
			var failure *contract.Problem
			if kind == "cleanup-failure" {
				failure = &contract.Problem{Code: "unavailable"}
			}
			h := newBoundaryHarness(t, NewClientWithTerminal(time.Second, boundaryPredicate), kind == "write-failure" || kind == "short-write", failure, kind == "write-failure", true)
			h.connection.writeShort = kind == "short-write"
			h.send(t, "first", 2)
			h.wait(t, h.connection.writeStarted.done)
			h.send(t, "next", 3)
			h.expectNoProblem(t)
			if kind == "parent-cancel" {
				h.cancel()
			}
			if kind == "session-done" {
				h.session.done.close()
			}
			h.connection.writeRelease.close()
			h.session.cleanup.close()
			select {
			case <-h.joined.done:
			case <-time.After(5 * time.Second):
				t.Fatal("failed/cancelled client did not join")
			}
			problem := <-h.result
			if h.session.calls.Load() != 1 {
				t.Fatal("failed round dispatched pending frame", h.session.calls.Load())
			}
			want := ""
			if kind == "cleanup-failure" {
				want = "unavailable"
			}
			if kind == "session-done" {
				want = "session_expired"
			}
			if want == "" && problem != nil || want != "" && (problem == nil || problem.Code != want) {
				t.Fatal("failure path changed", problem, want)
			}
			if want != "" {
				select {
				case code := <-h.notifications:
					if code != want {
						t.Fatal(code)
					}
				default:
					t.Fatal("failure problem missing")
				}
			}
			select {
			case code := <-h.notifications:
				t.Fatal("unexpected or duplicate notification", code)
			default:
			}
		})
	}
}

func TestClientWithTerminalClassifierKeepsGenuineMidstreamBusy(t *testing.T) {
	h := newBoundaryHarness(t, NewClientWithTerminal(time.Second, boundaryPredicate), false, nil, false, false)
	h.send(t, "first", 2)
	h.wait(t, h.session.delivered.done)
	h.send(t, "overlap", 3)
	select {
	case code := <-h.notifications:
		if code != "busy" {
			t.Fatal(code)
		}
	default:
		t.Fatal("midstream frame was accepted")
	}
	h.cancel()
	select {
	case <-h.joined.done:
	case <-time.After(5 * time.Second):
		t.Fatal("midstream client did not join")
	}
	if h.session.calls.Load() != 1 || h.session.maxActive.Load() != 1 {
		t.Fatal("midstream overlap reached Execute")
	}
}
