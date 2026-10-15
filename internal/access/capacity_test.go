package access

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testCapacity(t *testing.T, config CapacityConfig) *Capacity {
	t.Helper()
	c, err := NewCapacity(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func acquireRequest(t *testing.T, c *Capacity, p Principal) func() {
	t.Helper()
	release, err := c.AcquireRequest(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}
func requireCapacityRejection(t *testing.T, err error, dimension string, unavailable bool) {
	t.Helper()
	var decision *CapacityError
	if !errors.As(err, &decision) || decision.Dimension != dimension || decision.Unavailable != unavailable {
		t.Fatalf("expected %s unavailable=%v, got %v", dimension, unavailable, err)
	}
}
func requireNoConcurrency(t *testing.T, c *Capacity) {
	t.Helper()
	s := c.Snapshot()
	if s.Requests != 0 || s.Attempts != 0 || s.Users != 0 || s.Keys != 0 || s.Channels != 0 {
		t.Fatalf("leaked concurrency: %+v", s)
	}
}

func TestCapacityAtomicCeilingsAndUserIsolation(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 4, User: 2, Key: 1, Channel: 1})
	alice := Principal{UserID: 1, TokenID: 1}
	release1 := acquireRequest(t, c, alice)
	_, err := c.AcquireRequest(context.Background(), alice)
	requireCapacityRejection(t, err, "key concurrency", false)
	alice.TokenID = 2
	release2 := acquireRequest(t, c, alice)
	alice.TokenID = 3
	_, err = c.AcquireRequest(context.Background(), alice)
	requireCapacityRejection(t, err, "user concurrency", false)
	// Another Key cannot escape the user's ceiling, while another user can
	// still use the slots reserved by the stricter per-user ceiling.
	release3 := acquireRequest(t, c, Principal{UserID: 2, TokenID: 4})
	release4 := acquireRequest(t, c, Principal{UserID: 3, TokenID: 5})
	_, err = c.AcquireRequest(context.Background(), Principal{UserID: 4, TokenID: 6})
	requireCapacityRejection(t, err, "instance concurrency", true)
	if s := c.Snapshot(); s.Requests != 4 || s.Users != 3 || s.Keys != 4 {
		t.Fatalf("partial rejected reservation: %+v", s)
	}
	for _, release := range []func(){release1, release2, release3, release4} {
		release()
		release()
	}
	requireNoConcurrency(t, c)
}

func TestCapacityIndividualLimitsOnlyLowerServerCeilings(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 8, User: 4, Key: 3, Channel: 2})
	p := Principal{UserID: 1, TokenID: 1, UserConcurrencyLimit: 2, KeyConcurrencyLimit: 1}
	first := acquireRequest(t, c, p)
	_, err := c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "key concurrency", false)
	p.TokenID = 2
	second := acquireRequest(t, c, p)
	p.TokenID = 3
	_, err = c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "user concurrency", false)
	first()
	second()
	p.UserConcurrencyLimit = 100
	p.KeyConcurrencyLimit = 100
	var leases []func()
	for range 3 {
		leases = append(leases, acquireRequest(t, c, p))
	}
	_, err = c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "key concurrency", false)
	for _, release := range leases {
		release()
	}
	requireNoConcurrency(t, c)
}

func TestCapacityChannelReleaseCloseAndCanceledAcquire(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 4, User: 2, Key: 2, Channel: 1})
	request := acquireRequest(t, c, Principal{UserID: 1, TokenID: 1})
	first, err := c.AcquireAttempt(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.AcquireAttempt(context.Background(), 10)
	requireCapacityRejection(t, err, "channel concurrency", false)
	other, err := c.AcquireAttempt(context.Background(), 11)
	if err != nil {
		t.Fatal(err)
	}
	first()
	other()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.AcquireRequest(ctx, Principal{UserID: 2, TokenID: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if _, err := c.AcquireAttempt(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attempt: %v", err)
	}
	c.Close()
	if s := c.Snapshot(); s.Requests != 1 {
		t.Fatalf("close discarded a live reservation: %+v", s)
	}
	_, err = c.AcquireAttempt(context.Background(), 10)
	requireCapacityRejection(t, err, "closed", true)
	_, err = c.AcquireRequest(context.Background(), Principal{UserID: 2, TokenID: 2})
	requireCapacityRejection(t, err, "closed", true)
	request()
	requireNoConcurrency(t, c)
}

func TestCapacityUserRPMSharedAcrossKeysAndExpires(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	p := Principal{UserID: 1, TokenID: 1, UserRPMLimit: 2}
	acquireRequest(t, c, p)()
	now = now.Add(10 * time.Second)
	p.TokenID = 2
	acquireRequest(t, c, p)()
	p.TokenID = 3
	_, err := c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "user request rate", false)
	var decision *CapacityError
	errors.As(err, &decision)
	if decision.RetryAfterSeconds < 50 || decision.RetryAfterSeconds > 51 {
		t.Fatalf("unexpected retry: %+v", decision)
	}
	acquireRequest(t, c, Principal{UserID: 2, TokenID: 4, UserRPMLimit: 2})()
	now = now.Add(50 * time.Second)
	acquireRequest(t, c, p)() // exact oldest-event expiry frees one slot
	_, err = c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "user request rate", false)
	now = now.Add(time.Minute)
	if s := c.Snapshot(); s.RateUsers != 0 || s.RateEvents != 0 {
		t.Fatalf("expired rate state retained: %+v", s)
	}
	requireNoConcurrency(t, c)
}

func TestCapacityRateTrackerDoesNotEvictLiveLimits(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	for id := uint(1); id <= maxRateUsers; id++ {
		acquireRequest(t, c, Principal{UserID: id, TokenID: id, UserRPMLimit: 1})()
	}
	_, err := c.AcquireRequest(context.Background(), Principal{UserID: maxRateUsers + 1, TokenID: maxRateUsers + 1, UserRPMLimit: 1})
	requireCapacityRejection(t, err, "rate tracker", true)
	_, err = c.AcquireRequest(context.Background(), Principal{UserID: 1, TokenID: 1, UserRPMLimit: 1})
	requireCapacityRejection(t, err, "user request rate", false)
	now = now.Add(time.Minute)
	acquireRequest(t, c, Principal{UserID: maxRateUsers + 1, TokenID: maxRateUsers + 1, UserRPMLimit: 1})()
	requireNoConcurrency(t, c)
}

func TestCapacityParallelAcquisitionAndRepeatedRelease(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 16, User: 4, Key: 2, Channel: 2})
	var acquired, released sync.WaitGroup
	acquired.Add(128)
	released.Add(128)
	start := make(chan struct{})
	finish := make(chan struct{})
	for i := range 128 {
		go func(i int) {
			defer released.Done()
			<-start
			lease, err := c.AcquireRequest(context.Background(), Principal{UserID: 1, TokenID: uint(i%2 + 1)})
			acquired.Done()
			<-finish
			if err == nil {
				var duplicate sync.WaitGroup
				duplicate.Add(2)
				for range 2 {
					go func() { defer duplicate.Done(); lease() }()
				}
				duplicate.Wait()
			}
		}(i)
	}
	close(start)
	acquired.Wait()
	if s := c.Snapshot(); s.Requests != 4 || s.Users != 1 || s.Keys != 2 {
		t.Errorf("oversubscribed/partial reservations: %+v", s)
	}
	close(finish)
	released.Wait()
	requireNoConcurrency(t, c)
}

func TestCapacityRateEventMemoryBound(t *testing.T) {
	c := testCapacity(t, CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	p := Principal{UserID: 1, TokenID: 1, UserRPMLimit: maxRateEvents + 1}
	for range maxRateEvents {
		release, err := c.AcquireRequest(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	_, err := c.AcquireRequest(context.Background(), p)
	requireCapacityRejection(t, err, "rate tracker", true)
	if s := c.Snapshot(); s.RateEvents != maxRateEvents || s.Requests != 0 {
		t.Fatalf("tracker exceeded bound: %+v", s)
	}
	now = now.Add(time.Minute)
	acquireRequest(t, c, p)()
	if s := c.Snapshot(); s.RateEvents != 1 {
		t.Fatalf("expired events not reclaimed: %+v", s)
	}
}
