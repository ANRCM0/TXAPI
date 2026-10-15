package access

import (
	"context"
	"sync"
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
