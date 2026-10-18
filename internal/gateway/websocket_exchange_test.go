package gateway

import (
	"context"
	"errors"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"testing"
)

func TestWebSocketExchangeSharesRoundGuard(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	c := &roundConnection{frames: []v2.Frame{{Type: 1, Data: []byte("event")}}}
	done := make(chan v2.ExchangeResult, 1)
	go func() {
		done <- (WebSocketExchangeExecutor{}).ExecuteExchange(context.Background(), v2.Exchange{Upstream: c, Initial: v2.Frame{Data: []byte("create")}, Decode: func(v2.Frame) (v2.Envelope, error) {
			close(entered)
			<-finish
			return v2.Envelope{ProtocolTerminal: true}, nil
		}, Deliver: func(context.Context, v2.Frame) (int, error) { return 0, nil }})
	}()
	resourceWait(t, entered)
	rejected := (WebSocketRoundExecutor{}).ExecuteRound(context.Background(), v2.Round{Connection: c, Handle: func(context.Context, v2.Frame) ([]v2.Frame, bool, error) {
		t.Error("round overlapped exchange")
		return nil, true, nil
	}})
	close(finish)
	if rejected.Dispatch != v2.NotDispatched || (<-done).Termination != v2.Success {
		t.Fatal("connection guard did not cover both entry points")
	}
}

func TestWebSocketExchangeKeepsTerminalOnPartialDelivery(t *testing.T) {
	c := &roundConnection{frames: []v2.Frame{{Type: 1, Data: []byte("terminal")}}}
	n := int64(2)
	r := (WebSocketExchangeExecutor{}).ExecuteExchange(context.Background(), v2.Exchange{Upstream: c, Initial: v2.Frame{Data: []byte("create")}, Decode: func(v2.Frame) (v2.Envelope, error) {
		return v2.Envelope{Data: []byte("terminal"), ProtocolTerminal: true, Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: &n}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture"}}, nil
	}, Deliver: func(context.Context, v2.Frame) (int, error) { return 2, errors.New("partial") }})
	if !r.ProtocolComplete || !r.DeliveryCommitted || r.DeliveryBytesWritten != 2 || r.Termination != v2.ClientWriteError || r.Usage.Completeness != v2.Partial || *r.Usage.Counts.InputTokens != 2 {
		t.Fatalf("facts lost: %+v", r)
	}
}
