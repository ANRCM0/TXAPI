package websocket

import (
	"context"
	"errors"
	"io"
	"testing"
)

type exchangeConn struct {
	writes, reads int
	write         func(context.Context, Frame) (int, error)
}

func (c *exchangeConn) Write(ctx context.Context, f Frame) (int, error) {
	c.writes++
	if c.write != nil {
		return c.write(ctx, f)
	}
	return len(f.Data), nil
}
func (c *exchangeConn) Read(context.Context) (Frame, error) {
	c.reads++
	if c.reads > 1 {
		return Frame{}, io.EOF
	}
	return Frame{Type: 1, Data: []byte("event")}, nil
}

func TestExchangeSeparatesDispatchAndTerminalDelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"partial", 2, io.ErrClosedPipe}, {"zero", 0, io.ErrClosedPipe}, {"short", 2, nil}, {"invalid", 99, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &exchangeConn{}
			delivered := 0
			r := ExecuteExchange(context.Background(), Exchange{Upstream: c, Initial: Frame{Data: []byte("create")}, Decode: func(context.Context, Frame) ([]Frame, bool, error) { return []Frame{{Data: []byte("done")}}, true, nil }, Deliver: func(context.Context, Frame) (int, error) { delivered++; return tc.n, tc.err }})
			if c.writes != 1 || c.reads != 1 || delivered != 1 || r.Dispatch != ResponseReceived || !r.ProtocolComplete || !r.UpstreamWriteStarted || !r.DeliveryWriteStarted || r.Termination != ClientWriteError || r.Err == nil {
				t.Fatalf("incorrect facts: %+v", r)
			}
			want := uint64(0)
			if tc.n <= 4 {
				want = uint64(tc.n)
			}
			if r.DeliveryBytesWritten != want || r.UpstreamBytesWritten != 6 {
				t.Fatal(r)
			}
		})
	}
}
func TestExchangeNeverReplaysUncertainInitialWrite(t *testing.T) {
	for _, n := range []int{-1, 0, 2, 6, 7} {
		t.Run(string(rune('a'+n+1)), func(t *testing.T) {
			c := &exchangeConn{write: func(context.Context, Frame) (int, error) { return n, io.ErrClosedPipe }}
			r := ExecuteExchange(context.Background(), Exchange{Upstream: c, Initial: Frame{Data: []byte("create")}, Decode: func(context.Context, Frame) ([]Frame, bool, error) {
				t.Fatal("decoded failed dispatch")
				return nil, false, nil
			}, Deliver: func(context.Context, Frame) (int, error) { t.Fatal("delivered failed dispatch"); return 0, nil }})
			if c.writes != 1 || c.reads != 0 || r.Dispatch != Dispatching || !r.UpstreamWriteStarted || r.DeliveryWriteStarted || r.Termination != Failed || r.Err == nil {
				t.Fatal(r)
			}
		})
	}
}
func TestExchangeCancellationCannotBecomeSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &exchangeConn{}
	r := ExecuteExchange(ctx, Exchange{Upstream: c, Initial: Frame{Data: []byte("create")}, Decode: func(context.Context, Frame) ([]Frame, bool, error) {
		cancel()
		return []Frame{{Data: []byte("done")}}, true, nil
	}, Deliver: func(context.Context, Frame) (int, error) { t.Fatal("delivery after cancel"); return 0, nil }})
	if r.Termination != Canceled || !r.ProtocolComplete || r.DeliveryWriteStarted || !errors.Is(r.Err, context.Canceled) {
		t.Fatal(r)
	}
}
func TestExchangeCanceledBeforeWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &exchangeConn{}
	r := ExecuteExchange(ctx, Exchange{Upstream: c, Decode: func(context.Context, Frame) ([]Frame, bool, error) { return nil, true, nil }, Deliver: func(context.Context, Frame) (int, error) { return 0, nil }})
	if r.Dispatch != NotDispatched || r.UpstreamWriteStarted || c.writes != 0 || r.Termination != Canceled {
		t.Fatal(r)
	}
}
func TestExchangeReadEOFIsNotProtocolSuccess(t *testing.T) {
	c := &exchangeConn{}
	r := ExecuteExchange(context.Background(), Exchange{Upstream: c, Decode: func(context.Context, Frame) ([]Frame, bool, error) { return nil, false, nil }, Deliver: func(context.Context, Frame) (int, error) { return 0, nil }})
	if r.Termination != Failed || r.ProtocolComplete || !errors.Is(r.Err, io.EOF) || c.writes != 1 {
		t.Fatal(r)
	}
}
