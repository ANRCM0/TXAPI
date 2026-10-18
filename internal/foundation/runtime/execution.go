// Package runtime retains the assembly bridge during the state migration.
package runtime

import (
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
)

type Executor = gateway.PublicExecutor

func NewExecutor(g *gateway.Gateway) *Executor               { return gateway.NewPublicExecutor(g) }
func FromGateway(in gateway.RequestResult) execution.Outcome { return gateway.FromGateway(in) }
func ToCoreOutput(o execution.Output) core.Output            { return gateway.ToCoreOutput(o) }
