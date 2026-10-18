package gateway

import (
	"context"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

func (g *Gateway) admitGenerationRate(ctx context.Context, req execution.Request) (context.Context, int, int, *core.Error) {
	p, ok := access.PrincipalFromContext(ctx)
	if !ok || g.limiter == nil || access.HasRateAdmission(ctx, p.TokenID) {
		return ctx, 0, 0, nil
	}
	now := time.Now()
	if p.RPMLimit > 0 {
		if allowed, retry := g.limiter.AllowRequest(ctx, access.TokenBucketKey(p.TokenID), p.RPMLimit, now); !allowed {
			return ctx, 0, retry, &core.Error{StatusCode: 429, Error: core.ErrorDetail{Message: "API key request rate exceeded", Type: "rate_limit_error"}}
		}
	}
	estimate := 0
	if p.TPMLimit > 0 {
		estimate = billing.EstimateUsage(requestPromptText(req), "").PromptTokens
		if allowed, retry := g.limiter.AllowTokens(ctx, access.TokenUsageBucketKey(p.TokenID), p.TPMLimit, estimate, now); !allowed {
			return ctx, 0, retry, &core.Error{StatusCode: 429, Error: core.ErrorDetail{Message: "API key token rate exceeded", Type: "rate_limit_error"}}
		}
		ctx = access.WithTokenReservation(ctx, p.TokenID, estimate)
	}
	return access.WithRateAdmission(ctx, p.TokenID), estimate, 0, nil
}
