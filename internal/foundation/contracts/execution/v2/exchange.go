package execution

import (
	"context"
	"time"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
)

type FrameDelivery func(context.Context, Frame) (int, error)
type FrameDecoder func(Frame) (Envelope, error)

type Exchange struct {
	Upstream Connection
	ID       RoundID
	Deadline time.Time
	Initial  Frame
	Decode   FrameDecoder
	Deliver  FrameDelivery
}

type ExchangeResult struct {
	Dispatch             v1.Phase
	UpstreamBytesWritten uint64
	UpstreamWriteStarted bool
	DeliveryBytesWritten uint64
	DeliveryCommitted    bool
	ProtocolComplete     bool
	Termination          v1.Termination
	Usage                UsageObservation
	Err                  error
}

type ExchangeExecutor interface {
	ExecuteExchange(context.Context, Exchange) ExchangeResult
}
