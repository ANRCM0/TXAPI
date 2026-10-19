package backgroundstate

import (
	"context"

	bg "minigateway/internal/foundation/contracts/background/v1"
	bgv2 "minigateway/internal/foundation/contracts/background/v2"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

// RemoteCall and Progress are owner-only dispatch capabilities, not L3 APIs.
// Every network call validates its current fenced claim before acquiring I/O.
type RemoteCall struct {
	Action                    bg.RemoteAction
	Operation                 execution.Operation
	Body                      []byte
	Model, PublicID, RemoteID string
	Target                    *execution.TargetConstraint
	Stream                    bool
	After                     *uint64
}
type RateReservation struct {
	Epoch    string
	Estimate int
}
type Progress interface {
	Validate(context.Context, RemoteCall) error
	Dispatch(context.Context, execution.TargetConstraint, RateReservation) error
	Observe(context.Context, bg.Observation) error
}

type UsageProgress interface {
	ObserveUsage(context.Context, bgv2.Observation) error
}
type Remote interface {
	Execute(context.Context, RemoteCall, Progress) error
	FinishRate(context.Context, uint, RateReservation, *execution.Usage)
}
