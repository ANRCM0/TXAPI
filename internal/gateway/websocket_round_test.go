package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

type roundConnection struct {
	frames  []v2.Frame
	writes  []v2.Frame
	partial int
}

func (c *roundConnection) ID() v2.ConnectionID { return "connection-1" }
func (c *roundConnection) Read(context.Context) (v2.Frame, error) {
	if len(c.frames) == 0 {
		return v2.Frame{}, errors.New("input exhausted")
	}
	frame := c.frames[0]
	c.frames = c.frames[1:]
	return frame, nil
}
func (c *roundConnection) Write(_ context.Context, frame v2.Frame) (int, error) {
	c.writes = append(c.writes, frame)
	if c.partial > 0 {
		return c.partial, errors.New("closed")
	}
	return len(frame.Data), nil
}
func (c *roundConnection) Close() error { return nil }

func TestWebSocketRoundExecutorMapsFactsWithoutProtocolOwnership(t *testing.T) {
	connection := &roundConnection{frames: []v2.Frame{{Type: 1, Data: []byte("request")}}}
	result := (WebSocketRoundExecutor{}).ExecuteRound(context.Background(), v2.Round{Connection: connection, Handle: func(_ context.Context, frame v2.Frame) ([]v2.Frame, bool, error) {
		return []v2.Frame{{Type: frame.Type, Data: []byte("response")}}, true, nil
	}})
	if result.Dispatch != v2.ResponseReceived || result.Termination != v2.Success || !result.ProtocolComplete || result.BytesWritten != 8 || len(connection.writes) != 1 {
		t.Fatalf("round adapter altered ownership or facts: %+v", result)
	}
}

func TestRuntimeLeaseSetRetiresOnlyAfterLastRelease(t *testing.T) {
	set, err := NewRuntimeLeaseSet("r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	old, err := set.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r2"); err != nil {
		t.Fatal(err)
	}
	if set.Current() != "r2" || old.Revision() != "r1" {
		t.Fatalf("revision changed in place")
	}
	if err := old.Release(); err != nil {
		t.Fatal(err)
	}
	if err := old.Release(); err != nil {
		t.Fatal(err)
	}
	newLease, err := set.Acquire()
	if err != nil || newLease.Revision() != "r2" {
		t.Fatalf("new generation unavailable: %v", err)
	}
	if err := set.Publish("r3"); err != nil {
		t.Fatal(err)
	}
	if err := newLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLeaseSetRejectsRetiringOverflowAndKeepsCurrent(t *testing.T) {
	set, err := NewRuntimeLeaseSet("r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := set.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r2"); err != nil {
		t.Fatal(err)
	}
	second, err := set.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r3"); err == nil || set.Current() != "r2" {
		t.Fatalf("overflow changed current: %v %s", err, set.Current())
	}
	_ = first.Release()
	_ = second.Release()
}

func TestRuntimeLeaseSetRejectsReusingLiveRetiringRevision(t *testing.T) {
	set, err := NewRuntimeLeaseSet("r1", 2)
	if err != nil {
		t.Fatal(err)
	}
	first, err := set.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r2"); err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r1"); err == nil || set.Current() != "r2" {
		t.Fatalf("retiring revision was reused: %v, current=%s", err, set.Current())
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r1"); err != nil {
		t.Fatalf("fully drained revision remained reserved: %v", err)
	}
}

func TestWebSocketRoundsRejectOverlapAndReleaseAfterCompletion(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	connection := &roundConnection{frames: []v2.Frame{{Data: []byte("request")}, {Data: []byte("next")}}}
	finished := make(chan v2.RoundResult, 1)
	go func() {
		finished <- (WebSocketRoundExecutor{}).ExecuteRound(context.Background(), v2.Round{Connection: connection, Handle: func(context.Context, v2.Frame) ([]v2.Frame, bool, error) {
			close(entered)
			<-finish
			return nil, true, nil
		}})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first round did not start")
	}
	overlap := (WebSocketRoundExecutor{}).ExecuteRound(context.Background(), v2.Round{Connection: connection, Handle: func(context.Context, v2.Frame) ([]v2.Frame, bool, error) {
		t.Error("overlapping round invoked handler")
		return nil, true, nil
	}})
	close(finish)
	first := <-finished
	if overlap.Dispatch != v2.NotDispatched || overlap.Termination != v2.Failed || first.Termination != v2.Success {
		t.Fatalf("overlapping round was not isolated: first=%+v overlap=%+v", first, overlap)
	}
	next := (WebSocketRoundExecutor{}).ExecuteRound(context.Background(), v2.Round{Connection: connection, Handle: func(context.Context, v2.Frame) ([]v2.Frame, bool, error) {
		return nil, true, nil
	}})
	if next.Termination != v2.Success {
		t.Fatalf("completed round retained its connection: %+v", next)
	}
}

func TestRuntimeLeaseConcurrentReleaseDrainsExactlyOnce(t *testing.T) {
	set, err := NewRuntimeLeaseSet("r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := set.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Publish("r2"); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := lease.Release(); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if len(set.retiring) != 0 || lease.generation.refs != 0 || set.Current() != "r2" {
		t.Fatalf("concurrent release retained generation: %+v", set)
	}
}
