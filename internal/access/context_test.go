package access

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrincipalContextCopiesPolicyAndIgnoresStringKeys(t *testing.T) {
	original := Principal{UserID: 7, TokenID: 11, Policy: Policy{Mode: Allowlist, Models: []string{"m1"}}}
	ctx := WithPrincipal(context.Background(), original)
	original.Policy.Models[0] = "changed"
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Policy.Models[0] != "m1" {
		t.Fatalf("stored policy mutated: %+v", p)
	}
	p.Policy.Models[0] = "changed-again"
	p, _ = PrincipalFromContext(ctx)
	if p.Policy.Models[0] != "m1" {
		t.Fatal("retrieved policy aliases the context")
	}
	forged := context.WithValue(context.Background(), "user_id", uint(99))
	if _, ok := PrincipalFromContext(forged); ok {
		t.Fatal("string context forged authenticated identity")
	}
}

type completionLimiter struct {
	mu       sync.Mutex
	calls    int
	delta    int
	key      string
	canceled bool
}

func (l *completionLimiter) AllowRequest(context.Context, string, int, time.Time) (bool, int) {
	return true, 0
}
func (l *completionLimiter) AllowTokens(context.Context, string, int, int, time.Time) (bool, int) {
	return true, 0
}
func (l *completionLimiter) AddTokens(ctx context.Context, key string, delta int, _ time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.delta += delta
	l.key = key
	l.canceled = ctx.Err() != nil
}
func TestReservationFinalizesOnceAfterCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithTokenReservation(parent, 12, 100)
	cancel()
	limiter := &completionLimiter{}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() { defer group.Done(); FinishTokens(ctx, limiter, 30) }()
	}
	group.Wait()
	if limiter.calls != 1 || limiter.delta != -70 || limiter.key != "tok:12:tpm" || limiter.canceled {
		t.Fatalf("bad finalization: %+v", limiter)
	}
	FinishTokens(ctx, nil, 30)
}

func TestGenerationDoesNotInheritMutableAdmission(t *testing.T) {
	parent := WithRateAdmission(WithTokenReservation(context.Background(), 12, 100), 12)
	child := NewGenerationContext(parent)
	limiter := &completionLimiter{}
	FinishTokens(child, limiter, 0)
	if HasRateAdmission(child, 12) || limiter.calls != 0 {
		t.Fatal("child inherited parent admission")
	}
	FinishTokens(parent, limiter, 30)
	if !HasRateAdmission(parent, 12) || limiter.calls != 1 || limiter.delta != -70 {
		t.Fatal("child changed parent's admission")
	}
}

func TestHTTPAdmissionAndReservationHaveOneConsumer(t *testing.T) {
	parent := WithPrincipal(context.Background(), Principal{UserID: 7, TokenID: 12})
	parent = WithRateAdmission(WithTokenReservation(parent, 12, 100), 12)
	limiter := &completionLimiter{}
	var adopted atomic.Int64
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			child := NewHTTPGenerationContext(parent)
			if HasRateAdmission(child, 12) {
				adopted.Add(1)
			}
			FinishTokens(child, limiter, 30)
		}()
	}
	group.Wait()
	if adopted.Load() != 1 || limiter.calls != 1 || limiter.delta != -70 {
		t.Fatalf("adopted=%d limiter=%+v", adopted.Load(), limiter)
	}
	other := WithPrincipal(WithRateAdmission(context.Background(), 12), Principal{UserID: 9, TokenID: 13})
	if HasRateAdmission(NewHTTPGenerationContext(other), 12) {
		t.Fatal("HTTP admission crossed the authenticated key")
	}
	unauthenticated := WithRateAdmission(context.Background(), 12)
	if HasRateAdmission(NewHTTPGenerationContext(unauthenticated), 12) {
		t.Fatal("HTTP admission authenticated a missing principal")
	}
}
