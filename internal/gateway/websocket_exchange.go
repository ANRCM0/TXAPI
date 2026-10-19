package gateway

import (
	"context"
	"errors"

	"minigateway/internal/core"
	corews "minigateway/internal/core/websocket"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

type WebSocketExchangeExecutor struct{}

// Accepted protocol observations survive a panicking caller without inventing
// the mechanical terminal result that was never returned by the executor.
type websocketExchangeObservation struct {
	usage  v3.UsageObservation
	native v1.NativeCompletion
}

func (WebSocketExchangeExecutor) ExecuteExchange(ctx context.Context, in v2.Exchange) v2.ExchangeResult {
	observed := websocketExchangeObservation{}
	return executeWebSocketExchange(ctx, in, nil, &observed)
}

func executeWebSocketExchange(ctx context.Context, in v2.Exchange, decodeUsage func(v2.Frame) (v3.Envelope, error), observed *websocketExchangeObservation) v2.ExchangeResult {
	observed.usage = unknownDetailedUsageFact()
	r := v2.ExchangeResult{Dispatch: v2.NotDispatched, Termination: v2.Failed, Usage: unknownUsageFact()}
	if ctx == nil || nilInterface(in.Upstream) || in.Decode == nil || in.Deliver == nil {
		r.Err = errors.New("invalid websocket exchange")
		return r
	}
	if ctx.Err() != nil {
		r.Termination, r.Err = v2.Canceled, ctx.Err()
		return r
	}
	id := in.Upstream.ID()
	if id == "" {
		r.Err = errors.New("missing websocket connection identity")
		return r
	}
	if _, active := activeWebSocketRounds.LoadOrStore(id, struct{}{}); active {
		r.Err = errors.New("websocket connection is busy")
		return r
	}
	defer activeWebSocketRounds.Delete(id)
	facts := protocolFacts{usage: unknownUsageFact()}
	detailed := usageFacts{usage: unknownDetailedUsageFact()}
	out := corews.ExecuteExchange(ctx, corews.Exchange{
		Upstream: connectionAdapter{in.Upstream}, Deadline: in.Deadline,
		Initial: corews.Frame{Type: in.Initial.Type, Data: append([]byte(nil), in.Initial.Data...)},
		Decode: func(_ context.Context, frame corews.Frame) ([]corews.Frame, bool, error) {
			input := v2.Frame{Type: frame.Type, Data: append([]byte(nil), frame.Data...)}
			var envelope v2.Envelope
			var rich v3.Envelope
			var err error
			if decodeUsage != nil {
				rich, err = decodeUsage(input)
				envelope = rich.Execution
			} else {
				envelope, err = in.Decode(input)
				rich.Execution = envelope
				if envelope.Usage != nil {
					base := envelope.Usage
					rich.Usage = &v3.UsageObservation{Counts: base.Counts, Completeness: base.Completeness, Source: base.Source, Profile: base.Profile}
				}
			}
			if err != nil {
				return nil, false, err
			}
			var output core.Frame
			if decodeUsage != nil {
				output, err = detailed.accept(&facts, rich)
				observed.usage = cloneDetailedUsageFact(detailed.usage)
			} else {
				output, err = facts.accept(envelope)
				if err == nil && rich.Usage != nil {
					observed.usage = cloneDetailedUsageFact(*rich.Usage)
				}
			}
			if err != nil {
				return nil, false, err
			}
			if envelope.Native != nil {
				observed.native = *envelope.Native
			}
			var frames []corews.Frame
			if len(output.Data) != 0 {
				frames = []corews.Frame{{Type: frame.Type, Data: output.Data}}
			}
			return frames, envelope.ProtocolTerminal, nil
		},
		Deliver: func(ctx context.Context, frame corews.Frame) (int, error) {
			return in.Deliver(ctx, v2.Frame{Type: frame.Type, Data: append([]byte(nil), frame.Data...)})
		},
	})
	r.UpstreamBytesWritten, r.UpstreamWriteStarted = out.UpstreamBytesWritten, out.UpstreamWriteStarted
	r.DeliveryBytesWritten, r.DeliveryCommitted = out.DeliveryBytesWritten, out.DeliveryWriteStarted
	r.ProtocolComplete, r.Err, r.Usage = out.ProtocolComplete, out.Err, cloneUsageFact(facts.usage)
	switch out.Dispatch {
	case corews.NotDispatched:
		r.Dispatch = v2.NotDispatched
	case corews.Dispatching:
		r.Dispatch = v2.Dispatching
	case corews.ResponseReceived:
		r.Dispatch = v2.ResponseReceived
	default:
		r.Err = errors.New("unknown websocket dispatch fact")
		return r
	}
	switch out.Termination {
	case corews.Success:
		r.Termination = v1.Success
	case corews.Failed:
		r.Termination = v2.Failed
	case corews.Canceled:
		r.Termination = v1.Canceled
	case corews.ClientWriteError:
		r.Termination = v1.ClientWriteError
	default:
		r.Err = errors.New("unknown websocket termination fact")
		return r
	}
	if facts.failed && r.Termination == v1.Success {
		r.Termination = v2.Failed
	}
	return r
}

var _ v2.ExchangeExecutor = WebSocketExchangeExecutor{}
