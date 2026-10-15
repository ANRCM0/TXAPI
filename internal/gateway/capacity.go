package gateway

import (
	"context"
	"errors"

	"minigateway/internal/access"
)

// CapacityAdmission adapts process-local admission to the coordinator's two
// lease scopes. It reads limits only from the authenticated context snapshot.
type CapacityAdmission struct{ Capacity *access.Capacity }

func (a CapacityAdmission) Acquire(ctx context.Context, scope AdmissionScope) (func(), error) {
	if a.Capacity == nil {
		return nil, errors.New("capacity admission is not configured")
	}
	p, ok := access.PrincipalFromContext(ctx)
	if !ok || p.UserID == 0 || p.TokenID == 0 || p.UserID != scope.UserID || p.TokenID != scope.TokenID {
		return nil, errors.New("capacity admission requires the verified request identity")
	}
	var release func()
	var err error
	switch scope.Level {
	case RequestAdmission:
		release, err = a.Capacity.AcquireRequest(ctx, p)
	case AttemptAdmission:
		release, err = a.Capacity.AcquireAttempt(ctx, scope.ChannelID)
	default:
		return nil, errors.New("invalid capacity admission scope")
	}
	var rejection *access.CapacityError
	if errors.As(err, &rejection) {
		status := 429
		if rejection.Unavailable {
			status = 503
		}
		return release, &AdmissionError{StatusCode: status, RetryAfterSeconds: rejection.RetryAfterSeconds, Message: rejection.Error()}
	}
	return release, err
}
