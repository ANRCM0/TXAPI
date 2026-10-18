// Package responsesws defines the bounded native WebSocket application ports.
package responsesws

import (
	"context"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

const Version = "1"

type Limits struct {
	MaxMessageBytes int64
	IdleTimeout     time.Duration
	RoundTimeout    time.Duration
	MaxLifetime     time.Duration
}

type Problem struct {
	Code              string
	Message           string
	CloseCode         uint16
	RetryAfterSeconds int
}

type Open struct {
	Binding      identity.Binding
	ConnectionID execution.ConnectionID
}

type Create struct{ JSON []byte }
type Result struct {
	Facts   execution.ExchangeResult
	Problem *Problem
}

type Session interface {
	Execute(context.Context, Create, execution.FrameDelivery) Result
	Done() <-chan struct{}
	Problem() *Problem
	Close(context.Context) error
}

type Service interface {
	Open(context.Context, Open) (Session, *Problem)
	Limits() Limits
}

type ProblemDelivery func(context.Context, Problem) error

// Client coordinates one downstream reader and one active generation. Wire
// encoding and close frames remain the presentation adapter's responsibility.
type Client interface {
	Run(context.Context, Session, execution.Connection, ProblemDelivery) *Problem
}
