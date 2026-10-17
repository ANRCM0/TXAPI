package access

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Limiter is consumed by admission and completion. Implementations own their
// lifecycle and concurrency safety; callers pass only verified principal IDs.
type Limiter interface {
	AllowRequest(context.Context, string, int, time.Time) (bool, int)
	AllowTokens(context.Context, string, int, int, time.Time) (bool, int)
	AddTokens(context.Context, string, int, time.Time)
}

func TokenBucketKey(tokenID uint) string      { return "tok:" + strconv.FormatUint(uint64(tokenID), 10) }
func TokenUsageBucketKey(tokenID uint) string { return TokenBucketKey(tokenID) + ":tpm" }

type reservationKey struct{}
type rateAdmissionKey struct{}

func WithRateAdmission(ctx context.Context, tokenID uint) context.Context {
	return context.WithValue(ctx, rateAdmissionKey{}, tokenID)
}
func HasRateAdmission(ctx context.Context, tokenID uint) bool {
	id, _ := ctx.Value(rateAdmissionKey{}).(uint)
	return tokenID != 0 && id == tokenID
}

// A new generation never inherits a previous generation's mutable reservation.
func NewGenerationContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, reservationKey{}, (*tokenReservation)(nil))
	return context.WithValue(ctx, rateAdmissionKey{}, uint(0))
}

type tokenReservation struct {
	tokenID  uint
	estimate int
	once     sync.Once
}

// WithTokenReservation records a successful TPM pre-reservation. Completion
// refunds/charges the delta once even if separate finalizers call it twice.
func WithTokenReservation(ctx context.Context, tokenID uint, estimate int) context.Context {
	return context.WithValue(ctx, reservationKey{}, &tokenReservation{tokenID: tokenID, estimate: estimate})
}
func FinishTokens(ctx context.Context, limiter Limiter, actual int) {
	reservation, _ := ctx.Value(reservationKey{}).(*tokenReservation)
	if limiter == nil || reservation == nil {
		return
	}
	if actual < 0 {
		actual = 0
	}
	reservation.once.Do(func() {
		if delta := actual - reservation.estimate; delta != 0 {
			// Cancellation ends the call, not its already-acquired reservation.
			// A bounded independent context allows a remote limiter to release it.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			limiter.AddTokens(cleanup, TokenUsageBucketKey(reservation.tokenID), delta, time.Now())
		}
	})
}
