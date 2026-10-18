package gateway

import (
	"context"
	"sync"

	corews "minigateway/internal/core/websocket"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

type WebSocketRoundExecutor struct{}

var activeWebSocketRounds sync.Map

func (WebSocketRoundExecutor) ExecuteRound(ctx context.Context, round v2.Round) v2.RoundResult {
	result := v2.RoundResult{Dispatch: v2.NotDispatched, Termination: v2.Failed}
	if ctx == nil || nilInterface(round.Connection) || round.Handle == nil {
		return result
	}
	id := round.Connection.ID()
	if id == "" {
		return result
	}
	if ctx.Err() != nil {
		result.Termination = v2.Canceled
		return result
	}
	if _, active := activeWebSocketRounds.LoadOrStore(id, struct{}{}); active {
		return result
	}
	defer activeWebSocketRounds.Delete(id)
	mechanical := corews.Execute(ctx, corews.Round{
		Connection: connectionAdapter{round.Connection},
		Deadline:   round.Deadline,
		Handle: func(ctx context.Context, frame corews.Frame) ([]corews.Frame, bool, error) {
			outputs, complete, err := round.Handle(ctx, v2.Frame{Type: frame.Type, Data: append([]byte(nil), frame.Data...)})
			if err != nil {
				return nil, false, err
			}
			converted := make([]corews.Frame, len(outputs))
			for i, output := range outputs {
				converted[i] = corews.Frame{Type: output.Type, Data: append([]byte(nil), output.Data...)}
			}
			return converted, complete, nil
		},
	})
	result.Dispatch = v1.Phase(mechanical.Dispatch)
	result.FramesRead, result.FramesWritten = mechanical.FramesRead, mechanical.FramesWritten
	result.BytesWritten, result.Committed = mechanical.BytesWritten, mechanical.Committed
	result.ProtocolComplete = mechanical.ProtocolComplete
	result.Termination = v1.Termination(mechanical.Termination)
	return result
}

type connectionAdapter struct{ v2.Connection }

func (c connectionAdapter) Read(ctx context.Context) (corews.Frame, error) {
	frame, err := c.Connection.Read(ctx)
	return corews.Frame{Type: frame.Type, Data: append([]byte(nil), frame.Data...)}, err
}
func (c connectionAdapter) Write(ctx context.Context, frame corews.Frame) (int, error) {
	return c.Connection.Write(ctx, v2.Frame{Type: frame.Type, Data: append([]byte(nil), frame.Data...)})
}

var _ v2.RoundExecutor = WebSocketRoundExecutor{}
