package access

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConnectionCapacityCountsPhysicalConnectionsNotGenerations(t *testing.T) {
	c, err := NewConnectionCapacity(ConnectionLimits{Instance: 4, User: 2, Key: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 1}); err == nil {
		t.Fatal("one Key exceeded connection ceiling")
	}
	second, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 3}); err == nil {
		t.Fatal("multiple Keys bypassed owner ceiling")
	}
	third, err := c.Acquire(context.Background(), Binding{UserID: 2, TokenID: 4})
	if err != nil {
		t.Fatal("other owner was starved", err)
	}
	fourth, err := c.Acquire(context.Background(), Binding{UserID: 3, TokenID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), Binding{UserID: 4, TokenID: 6}); err == nil {
		t.Fatal("instance ceiling bypassed")
	}
	c.Close()
	if got := c.Snapshot(); got != (ConnectionSnapshot{Active: 4, Users: 3, Keys: 4}) {
		t.Fatalf("close discarded occupied leases: %+v", got)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); first(); second(); third(); fourth() }()
	}
	wg.Wait()
	if got := c.Snapshot(); got != (ConnectionSnapshot{}) {
		t.Fatalf("connection leaks/double release: %+v", got)
	}
	if _, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 1}); err == nil {
		t.Fatal("closed limiter admitted a connection")
	}
}

func TestConnectionCapacityConcurrentAcquisitionAndCancellation(t *testing.T) {
	c, err := NewConnectionCapacity(ConnectionLimits{Instance: 10, User: 5, Key: 3})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var accepted atomic.Int64
	releases := make(chan func(), 64)
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := c.Acquire(context.Background(), Binding{UserID: 1, TokenID: 1})
			if err == nil {
				accepted.Add(1)
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	if accepted.Load() != 3 {
		t.Fatalf("non-atomic connection ceiling: %d", accepted.Load())
	}
	for release := range releases {
		release()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Acquire(ctx, Binding{UserID: 2, TokenID: 2}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), Binding{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if c.Snapshot().Active != 0 {
		t.Fatal("refused connections consumed capacity")
	}
	for _, limits := range []ConnectionLimits{{}, {1, 1, 1}, {4097, 2, 1}, {4, 4, 1}, {4, 2, 3}} {
		if _, err := NewConnectionCapacity(limits); err == nil {
			t.Fatalf("invalid limits accepted: %+v", limits)
		}
	}
}
