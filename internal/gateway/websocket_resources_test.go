package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type trackedWebSocketResource struct {
	calls  atomic.Int32
	fail   atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (r *trackedWebSocketResource) Close(context.Context) error {
	r.calls.Add(1)
	if r.fail.Load() {
		return errors.New("fixture close failed")
	}
	r.once.Do(func() { close(r.closed) })
	return nil
}
func resourceFixture() *trackedWebSocketResource {
	return &trackedWebSocketResource{closed: make(chan struct{})}
}
func publishResource(t *testing.T, o *websocketResources, rev string, r websocketResource) {
	t.Helper()
	if err := o.prepare(context.Background(), rev, func(context.Context) (websocketResource, error) { return r, nil }); err != nil {
		t.Fatal(err)
	}
	if err := o.publish(rev, nil); err != nil {
		t.Fatal(err)
	}
}
func resourceWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resource did not close")
	}
}
func TestWebSocketResourcesPinRetiredResourcesAndDrain(t *testing.T) {
	o := newWebSocketResources()
	r1, r2 := resourceFixture(), resourceFixture()
	publishResource(t, o, "r1", r1)
	lease, err := o.acquire()
	if err != nil {
		t.Fatal(err)
	}
	publishResource(t, o, "r2", r2)
	if r1.calls.Load() != 0 {
		t.Fatal("resource closed before last consumer")
	}
	if err := o.prepare(context.Background(), "r1", func(context.Context) (websocketResource, error) {
		t.Fatal("retiring revision was prepared")
		return nil, nil
	}); err == nil {
		t.Fatal("retiring identity reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if o.drain(ctx) == nil {
		t.Fatal("drain ignored live lease")
	}
	if r1.calls.Load() != 0 {
		t.Fatal("drain killed a pinned resource")
	}
	if _, err := o.acquire(); err == nil {
		t.Fatal("draining admitted new round")
	}
	_ = lease.Release()
	_ = lease.Release()
	resourceWait(t, r1.closed)
	resourceWait(t, r2.closed)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if r1.calls.Load() != 1 || r2.calls.Load() != 1 {
		t.Fatal("duplicate resource close")
	}
}
func TestWebSocketResourcesFailedCandidateAndCloseRetry(t *testing.T) {
	o := newWebSocketResources()
	running := resourceFixture()
	publishResource(t, o, "r1", running)
	partial := resourceFixture()
	partial.fail.Store(true)
	if o.prepare(context.Background(), "bad", func(context.Context) (websocketResource, error) { return partial, errors.New("prepare failed") }) == nil {
		t.Fatal("prepare accepted failure")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		o.mu.Lock()
		failed := o.candidate != nil && o.candidate.closeErr != nil
		o.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if o.publish("bad", nil) == nil {
		t.Fatal("failed candidate published")
	}
	lease, err := o.acquire()
	if err != nil || lease.Revision() != "r1" {
		t.Fatal("failed candidate changed current")
	}
	_ = lease.Release()
	partial.fail.Store(false)
	if err := o.recoverCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, partial.closed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if partial.calls.Load() != 2 {
		t.Fatal("failed close not retried exactly once")
	}
}
func TestWebSocketResourcesBoundRetirementAndFailBeforeApply(t *testing.T) {
	o := newWebSocketResources()
	var leases []interface{ Release() error }
	for _, rev := range []string{"r1", "r2", "r3"} {
		publishResource(t, o, rev, resourceFixture())
		lease, _ := o.acquire()
		leases = append(leases, lease)
	}
	candidate := resourceFixture()
	if err := o.prepare(context.Background(), "r4", func(context.Context) (websocketResource, error) { return candidate, nil }); err != nil {
		t.Fatal(err)
	}
	called := false
	if o.publish("r4", func() error { called = true; return nil }) == nil || called {
		t.Fatal("capacity failure reached apply")
	}
	if o.publish("r3", nil) != nil {
		t.Fatal("same current revision not idempotent")
	}
	o.discard()
	resourceWait(t, candidate.closed)
	for _, lease := range leases {
		_ = lease.Release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketResourcesDrainJoinsPreparingCandidate(t *testing.T) {
	o := newWebSocketResources()
	entered, finish := make(chan struct{}), make(chan struct{})
	prepared := make(chan error, 1)
	candidate := resourceFixture()
	go func() {
		prepared <- o.prepare(context.Background(), "r1", func(context.Context) (websocketResource, error) { close(entered); <-finish; return candidate, nil })
	}()
	resourceWait(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if o.drain(ctx) == nil {
		t.Fatal("drain claimed preparation had joined")
	}
	close(finish)
	if err := <-prepared; err == nil {
		t.Fatal("candidate published after drain")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.drain(ctx); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, candidate.closed)
	if o.publish("r1", nil) == nil {
		t.Fatal("draining owner accepted publication")
	}
}

func waitWebSocketOwnerCleanup(t *testing.T, o *websocketResources) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.mu.Lock()
	for {
		closing := o.candidate != nil && o.candidate.closing
		for _, g := range o.retired {
			closing = closing || g.closing
		}
		if !closing {
			o.mu.Unlock()
			return
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("owner close worker did not join")
		}
		o.mu.Lock()
	}
}
