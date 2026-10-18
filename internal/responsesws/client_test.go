package responsesws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
)

type clientConn struct {
	mu      sync.Mutex
	frames  []execution.Frame
	writes  []execution.Frame
	closed  chan struct{}
	written chan struct{}
}

func (c *clientConn) ID() execution.ConnectionID { return "client" }
func (c *clientConn) Read(ctx context.Context) (execution.Frame, error) {
	c.mu.Lock()
	if len(c.frames) > 0 {
		f := c.frames[0]
		c.frames = c.frames[1:]
		c.mu.Unlock()
		return f, nil
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return execution.Frame{}, ctx.Err()
	case <-c.closed:
		return execution.Frame{}, errors.New("closed")
	}
}
func (c *clientConn) Write(_ context.Context, f execution.Frame) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, f)
	c.mu.Unlock()
	if c.written != nil {
		select {
		case c.written <- struct{}{}:
		default:
		}
	}
	return len(f.Data), nil
}
func (c *clientConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type clientSession struct {
	done    chan struct{}
	problem *contract.Problem
	active  chan struct{}
	finish  chan struct{}
}

func (s *clientSession) Execute(ctx context.Context, _ contract.Create, d execution.FrameDelivery) contract.Result {
	select {
	case s.active <- struct{}{}:
	default:
		return contract.Result{Problem: &contract.Problem{Code: "busy"}}
	}
	defer func() {
		select {
		case <-s.active:
		default:
		}
	}()
	select {
	case <-s.finish:
	case <-ctx.Done():
		return contract.Result{Problem: &contract.Problem{Code: "session_expired"}}
	}
	_, _ = d(ctx, execution.Frame{Type: 1, Data: []byte("ok")})
	return contract.Result{}
}
func (s *clientSession) Done() <-chan struct{}      { return s.done }
func (s *clientSession) Problem() *contract.Problem { return s.problem }
func (s *clientSession) Close(context.Context) error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func TestClientRejectsBusyAndKeepsOneReader(t *testing.T) {
	conn := &clientConn{frames: []execution.Frame{{Type: 1, Data: []byte("a")}, {Type: 1, Data: []byte("b")}}, closed: make(chan struct{}), written: make(chan struct{}, 1)}
	session := &clientSession{done: make(chan struct{}), active: make(chan struct{}, 1), finish: make(chan struct{})}
	notifications := make(chan string, 4)
	client := NewClient(time.Second)
	done := make(chan *contract.Problem, 1)
	go func() {
		done <- client.Run(context.Background(), session, conn, func(_ context.Context, p contract.Problem) error { notifications <- p.Code; return nil })
	}()
	select {
	case code := <-notifications:
		if code != "busy" {
			t.Fatalf("notification=%s", code)
		}
	case <-time.After(time.Second):
		t.Fatal("busy notification missing")
	}
	close(session.finish)
	select {
	case <-conn.written:
	case <-time.After(time.Second):
		t.Fatal("active generation did not deliver")
	}
	close(conn.closed)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not join reader")
	}
	conn.mu.Lock()
	writes := len(conn.writes)
	conn.mu.Unlock()
	if writes != 1 {
		t.Fatalf("writes=%d want 1", writes)
	}
}
func TestClientExpiresIdleSessionAndNotifiesOnce(t *testing.T) {
	conn := &clientConn{closed: make(chan struct{})}
	session := &clientSession{done: make(chan struct{}), active: make(chan struct{}, 1), finish: make(chan struct{})}
	notifications := make(chan string, 2)
	got := NewClient(20*time.Millisecond).Run(context.Background(), session, conn, func(_ context.Context, p contract.Problem) error { notifications <- p.Code; return nil })
	if got == nil || got.Code != "session_expired" {
		t.Fatalf("problem=%+v", got)
	}
	select {
	case code := <-notifications:
		if code != "session_expired" {
			t.Fatal(code)
		}
	default:
		t.Fatal("missing notification")
	}
}
