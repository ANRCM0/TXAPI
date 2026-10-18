package access

import (
	"context"
	"fmt"
	admission "minigateway/internal/foundation/contracts/admission/v1"
	"time"
)

type requestRateGate struct{ limiter Limiter }

func NewRateGate(limiter Limiter) admission.RequestGate {
	if limiter == nil {
		return nil
	}
	return &requestRateGate{limiter: limiter}
}
func (g *requestRateGate) Admit(ctx context.Context, estimate func() int) (context.Context, admission.RateDecision) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return ctx, admission.RateDecision{}
	}
	now := time.Now()
	decision := admission.RateDecision{}
	if p.RPMLimit > 0 {
		allowed, retry := g.limiter.AllowRequest(ctx, TokenBucketKey(p.TokenID), p.RPMLimit, now)
		if !allowed {
			return ctx, admission.RateDecision{Rejected: true, RetryAfterSeconds: retry, Message: fmt.Sprintf("\u4ee4\u724c %q \u6bcf\u5206\u949f\u8bf7\u6c42\u6570\u8d85\u8fc7\u4e0a\u9650 %d", p.TokenName, p.RPMLimit)}
		}
	}
	if p.TPMLimit > 0 {
		count := 0
		if estimate != nil {
			count = estimate()
		}
		if count < 0 {
			count = 0
		}
		allowed, retry := g.limiter.AllowTokens(ctx, TokenUsageBucketKey(p.TokenID), p.TPMLimit, count, now)
		if !allowed {
			return ctx, admission.RateDecision{Rejected: true, RetryAfterSeconds: retry, Message: fmt.Sprintf("\u4ee4\u724c %q \u6bcf\u5206\u949f token \u6570\u8d85\u8fc7\u4e0a\u9650 %d\uff08\u672c\u6b21\u9884\u4f30 %d\uff09", p.TokenName, p.TPMLimit, count)}
		}
		ctx = WithTokenReservation(ctx, p.TokenID, count)
		decision.Estimate, decision.HasEstimate = count, true
	}
	return WithRateAdmission(ctx, p.TokenID), decision
}
func (g *requestRateGate) Finish(ctx context.Context, actual int) {
	FinishTokens(ctx, g.limiter, actual)
}
