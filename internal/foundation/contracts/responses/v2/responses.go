// Package responses adds native background commands without changing v1.
package responses

import (
	"context"
	appv1 "minigateway/internal/foundation/contracts/responses/v1"
)

const Version = "2"
const CancelResponse appv1.Action = "response.cancel"

type Command struct {
	Base          appv1.Command
	Stream        bool
	StartingAfter *uint64
}
type Call interface {
	Handle(Command, appv1.Delivery)
	Close()
}
type Service interface {
	Begin(context.Context, appv1.Action) (Call, *appv1.Problem)
}
