package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResponsesWSMainJoinRetriesCloseBeforeSharedDependencyTeardown(t *testing.T) {
	f := newWSAssemblyFixture(t)
	c := f.dial(t)
	f.send(t, c, "", "hello", false)
	f.receiveRound(t, c)
	resource := f.resources[0]
	failed, retrying, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var allowOnce sync.Once
	allow := func() { allowOnce.Do(func() { close(release) }) }
	t.Cleanup(allow)
	var calls atomic.Int32
	resource.beforeClose = func(ctx context.Context) error {
		switch calls.Add(1) {
		case 1:
			close(failed)
			return errors.New("fixture resource close failed")
		case 2:
			close(retrying)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	joined := make(chan struct{})
	go func() {
		joinResponsesWSRuntime(f.runtime)
		close(joined)
	}()
	wsWait(t, failed)
	wsWait(t, retrying)
	select {
	case <-joined:
		t.Fatal("main advanced to dependency teardown before resource cleanup joined")
	case <-resource.closed:
		t.Fatal("failed resource was reported closed before retry completed")
	case <-time.After(20 * time.Millisecond):
	}
	if err := f.st.DB().Exec("SELECT 1").Error; err != nil {
		t.Fatal("shared database unavailable during cleanup retry", err)
	}
	allow()
	wsWait(t, joined)
	wsWait(t, resource.closed)
	if calls.Load() != 2 {
		t.Fatal("unexpected number of resource close calls", calls.Load())
	}
	if snapshot := f.capacity.Snapshot(); snapshot.Requests != 0 || snapshot.Attempts != 0 {
		t.Fatal("main join returned before admission release", snapshot)
	}
	if f.attempts.Load() != 1 || f.httpCalls.Load() != 0 {
		t.Fatal("cleanup recovery replayed inference")
	}
}
