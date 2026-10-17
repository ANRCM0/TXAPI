package gateway

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

// Admission is the S3 implementation seam. It does not authorize models or
// select channels. Acquire observes the request deadline and must return when
// canceled. A successful acquisition must return a non-nil release callback.
// release is called exactly once before this scope exits, including panic paths.
// It must be fast, concurrency-safe and must not depend on a canceled context.
type Admission interface {
	Acquire(context.Context, AdmissionScope) (release func(), err error)
}

type AdmissionLevel string

const (
	RequestAdmission AdmissionLevel = "request"
	AttemptAdmission AdmissionLevel = "attempt"
)

type AdmissionScope struct {
	Level        AdmissionLevel
	Operation    protocol.Operation
	UserID       uint
	TokenID      uint
	Model        string
	ChannelID    uint
	CredentialID uint
	Attempt      int
}

// AdmissionError is safe to report to callers: 429 for a configured limit and
// 503 for temporary capacity. Unknown failures never expose backend error text.
type AdmissionError struct {
	StatusCode        int
	RetryAfterSeconds int
	Message           string
}

func (e *AdmissionError) Error() string { return e.Message }

func (g *Gateway) acquire(ctx context.Context, output *executionOutput, scope AdmissionScope) (func(), *core.Error) {
	noop := func() {}
	if err := ctx.Err(); err != nil {
		return noop, admissionFailure(ctx, nil)
	}
	if g.admission == nil {
		return noop, nil
	}
	release, err := g.admission.Acquire(ctx, scope)
	var once sync.Once
	cleanup := func() {
		if release != nil {
			once.Do(release)
		}
	}
	if err != nil || ctx.Err() != nil {
		cleanup()
		var rejection *AdmissionError
		if errors.As(err, &rejection) && rejection.RetryAfterSeconds > 0 {
			output.setMetadata("Retry-After", strconv.Itoa(rejection.RetryAfterSeconds))
		}
		return noop, admissionFailure(ctx, err)
	}
	if release == nil {
		return noop, &core.Error{StatusCode: 503, Error: core.ErrorDetail{Message: "admission returned no lease", Type: "admission_error"}}
	}
	return cleanup, nil
}
func admissionFailure(ctx context.Context, err error) *core.Error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &core.Error{StatusCode: 499, Error: core.ErrorDetail{Message: "request canceled", Type: "canceled"}}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &core.Error{StatusCode: 504, Error: core.ErrorDetail{Message: "request time budget exhausted", Type: "deadline"}}
	}
	status := 503
	message := "admission temporarily unavailable"
	var rejection *AdmissionError
	if errors.As(err, &rejection) {
		if rejection.StatusCode == 429 {
			status = 429
		}
		if rejection.Message != "" {
			message = rejection.Message
		}
	}
	return &core.Error{StatusCode: status, Error: core.ErrorDetail{Message: message, Type: "admission_error"}}
}
