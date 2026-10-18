// Package admission defines rate capabilities without mutable context markers.
package admission

import (
	"context"
	"time"
)

const Version = "1"

type Limiter interface {
	AllowRequest(context.Context, string, int, time.Time) (bool, int)
	AllowTokens(context.Context, string, int, int, time.Time) (bool, int)
	AddTokens(context.Context, string, int, time.Time)
}
type RateDecision struct {
	Rejected          bool
	RetryAfterSeconds int
	Message           string
	Estimate          int
	HasEstimate       bool
}

// Estimate is invoked only after RPM admission, and only when TPM is configured.
// It must be synchronous and bounded; the implementation owns admission markers.
type RequestGate interface {
	Admit(context.Context, func() int) (context.Context, RateDecision)
	Finish(context.Context, int)
}
