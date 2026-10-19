package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

type cleanupNativeResource struct {
	*trackedWebSocketResource
	block   <-chan struct{}
	entered chan struct{}
}

func (r *cleanupNativeResource) Close(ctx context.Context) error {
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.trackedWebSocketResource.Close(ctx)
}

func (r *cleanupNativeResource) Dial(context.Context, string) (v2.Connection, error) {
	return nil, errors.New("cleanup fixture does not dial")
}

func TestNativePublicationRecoversFailedCandidateCleanup(t *testing.T) {
	f := newNativeServiceFixture(t)
	f.publish(t)
	old := f.service.resources.current
	partial := &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}
	partial.fail.Store(true)
	defer partial.fail.Store(false)
	var prepares atomic.Int32
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) {
		prepares.Add(1)
		return partial, errors.New("partial prepare")
	}
	f.channels[0].Name = "next"
	if f.service.Publish(context.Background(), f.channels) == nil {
		t.Fatal("partial prepare accepted")
	}
	waitWebSocketOwnerCleanup(t, f.service.resources)
	// A failed retry must retain both the failed candidate and old current.
	if f.service.Publish(context.Background(), f.channels) == nil {
		t.Fatal("failed cleanup accepted")
	}
	if prepares.Load() != 1 || partial.calls.Load() != 2 || f.service.resources.current != old {
		t.Fatal("cleanup was not retried exactly once before prepare", prepares.Load(), partial.calls.Load())
	}
	partial.fail.Store(false)
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) {
		prepares.Add(1)
		return &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}, nil
	}
	if err := f.service.Publish(context.Background(), f.channels); err != nil {
		t.Fatal("publication did not recover failed cleanup", err)
	}
	if prepares.Load() != 2 || partial.calls.Load() != 3 || f.service.resources.current == old {
		t.Fatal("candidate was reused before cleanup completed")
	}
}

func TestNativePublicationRecoversFailedRetiredCleanupWithoutClosingPinned(t *testing.T) {
	f := newNativeServiceFixture(t)
	retired := &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}
	retired.fail.Store(true)
	defer retired.fail.Store(false)
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) { return retired, nil }
	f.publish(t)
	pinned, err := f.service.resources.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Release()
	current := &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) { return current, nil }
	f.channels[0].Name = "second"
	f.publish(t)
	if retired.calls.Load() != 0 {
		t.Fatal("pinned retired generation closed")
	}
	_ = pinned.Release()
	waitWebSocketOwnerCleanup(t, f.service.resources)
	var prepares atomic.Int32
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) {
		prepares.Add(1)
		return &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}, nil
	}
	f.channels[0].Name = "third"
	if f.service.Publish(context.Background(), f.channels) == nil || retired.calls.Load() != 2 || prepares.Load() != 0 || current.calls.Load() != 0 {
		t.Fatal("failed retired cleanup advanced publication or closed current")
	}
	retired.fail.Store(false)
	f.publish(t)
	if retired.calls.Load() != 3 || prepares.Load() != 1 {
		t.Fatal("retired cleanup did not recover")
	}
}

func TestNativePublicationCleanupTimeoutRetainsCandidateAndDoesNotPrepare(t *testing.T) {
	f := newNativeServiceFixture(t)
	f.publish(t)
	old := f.service.resources.current
	partial := &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}
	partial.fail.Store(true)
	defer partial.fail.Store(false)
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) { return partial, errors.New("partial prepare") }
	f.channels[0].Name = "next"
	if f.service.Publish(context.Background(), f.channels) == nil {
		t.Fatal("partial prepare accepted")
	}
	waitWebSocketOwnerCleanup(t, f.service.resources)
	release := make(chan struct{})
	defer close(release)
	partial.block, partial.entered = release, make(chan struct{}, 1)
	partial.fail.Store(false)
	var prepares atomic.Int32
	f.service.options.Prepare = func(context.Context) (NativeWSResource, error) {
		prepares.Add(1)
		return &cleanupNativeResource{trackedWebSocketResource: resourceFixture()}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.service.Publish(ctx, f.channels); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	f.service.resources.mu.Lock()
	retained := f.service.resources.candidate != nil && f.service.resources.candidate.closing && f.service.resources.current == old
	f.service.resources.mu.Unlock()
	if !retained || prepares.Load() != 0 {
		t.Fatal("timeout lost ownership or ran factory")
	}
	// A later publication waits for the same close worker instead of restarting it.
	done := make(chan error, 1)
	go func() { done <- f.service.Publish(context.Background(), f.channels) }()
	select {
	case err := <-done:
		t.Fatal("publication passed blocked cleanup", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Release without closing twice in the deferred cleanup.
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if partial.calls.Load() != 2 || prepares.Load() != 1 {
		t.Fatal("cleanup was duplicated")
	}
}

func TestWebSocketResourcesPrepareCancellationCleansPartialCandidate(t *testing.T) {
	o := newWebSocketResources()
	running, partial := resourceFixture(), resourceFixture()
	publishResource(t, o, "old", running)
	ctx, cancel := context.WithCancel(context.Background())
	if err := o.prepare(ctx, "new", func(preparing context.Context) (websocketResource, error) {
		if _, bounded := preparing.Deadline(); !bounded {
			t.Fatal("prepare is unbounded")
		}
		cancel()
		return partial, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	resourceWait(t, partial.closed)
	if running.calls.Load() != 0 {
		t.Fatal("canceled preparation closed current")
	}
	if err := o.drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketResourceConcurrentDuplicateReleaseClosesOnce(t *testing.T) {
	o := newWebSocketResources()
	old := resourceFixture()
	publishResource(t, o, "old", old)
	lease, err := o.acquire()
	if err != nil {
		t.Fatal(err)
	}
	publishResource(t, o, "new", resourceFixture())
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() { defer group.Done(); _ = lease.Release() }()
	}
	group.Wait()
	resourceWait(t, old.closed)
	if err := o.drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if old.calls.Load() != 1 {
		t.Fatal("duplicate close", old.calls.Load())
	}
}

func TestNativeSessionShortCloseRetainsLeaseUntilCallbackJoins(t *testing.T) {
	f := newNativeServiceFixture(t)
	f.publish(t)
	s := f.open(t, "short-close")
	entered, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var releaseFinish sync.Once
	defer releaseFinish.Do(func() { close(finish) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Execute(f.trusted, nativeCreate(""), func(_ context.Context, frame v2.Frame) (int, error) {
			once.Do(func() { close(entered); <-finish })
			return len(frame.Data), nil
		})
	}()
	resourceWait(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	f.service.resources.mu.Lock()
	refs := f.service.resources.current.refs
	f.service.resources.mu.Unlock()
	if refs != 1 || f.service.capacity.Snapshot().Active != 1 {
		t.Fatal("short close released active ownership")
	}
	select {
	case <-s.Done():
		t.Fatal("short close declared joined")
	default:
	}
	releaseFinish.Do(func() { close(finish) })
	resourceWait(t, done)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, s.Done())
	if f.service.capacity.Snapshot().Active != 0 {
		t.Fatal("retry leaked session")
	}
}
