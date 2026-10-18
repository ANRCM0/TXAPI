package websocket

import (
	"context"
	"errors"
	"testing"
)

type fakeConnection struct {
	frames    []Frame
	writes    []Frame
	partial   int
	writeErr  error
	zeroWrite bool
	closeCall int
}

func (c *fakeConnection) Read(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if len(c.frames) == 0 {
		return Frame{}, errors.New("input exhausted")
	}
	frame := c.frames[0]
	c.frames = c.frames[1:]
	return frame, nil
}
func (c *fakeConnection) Write(_ context.Context, frame Frame) (int, error) {
	c.writes = append(c.writes, frame)
	if c.zeroWrite {
		return 0, c.writeErr
	}
	if c.partial > 0 {
		return c.partial, c.writeErr
	}
	return len(frame.Data), c.writeErr
}

func TestExecuteRoundRecordsProtocolAndDeliveryFacts(t *testing.T) {
	connection := &fakeConnection{frames: []Frame{{Type: 1, Data: []byte("request")}}}
	result := Execute(context.Background(), Round{Connection: connection, Handle: func(_ context.Context, frame Frame) ([]Frame, bool, error) {
		if string(frame.Data) != "request" {
			t.Fatal("request frame changed")
		}
		return []Frame{{Type: 1, Data: []byte("response")}}, true, nil
	}})
	if result.Dispatch != ResponseReceived || result.Termination != Success || !result.ProtocolComplete || !result.Committed ||
		result.FramesRead != 1 || result.FramesWritten != 1 || result.BytesWritten != 8 || len(connection.writes) != 1 {
		t.Fatalf("round facts incomplete: %+v", result)
	}
}

func TestExecuteRoundPreservesPartialWriteAndDoesNotInventTerminal(t *testing.T) {
	connection := &fakeConnection{frames: []Frame{{Data: []byte("request")}}, partial: 2, writeErr: errors.New("closed")}
	result := Execute(context.Background(), Round{Connection: connection, Handle: func(context.Context, Frame) ([]Frame, bool, error) {
		return []Frame{{Data: []byte("response")}}, true, nil
	}})
	if result.Termination != ClientWriteError || !result.Committed || result.BytesWritten != 2 || result.FramesWritten != 0 || result.ProtocolComplete {
		t.Fatalf("partial write was reported as complete: %+v", result)
	}
}

func TestExecuteRoundRejectsShortWriteWithoutError(t *testing.T) {
	connection := &fakeConnection{frames: []Frame{{Data: []byte("request")}}, partial: 2}
	result := Execute(context.Background(), Round{Connection: connection, Handle: func(context.Context, Frame) ([]Frame, bool, error) {
		return []Frame{{Data: []byte("response")}, {Data: []byte("must not write")}}, true, nil
	}})
	if result.Dispatch != Dispatching || result.Termination != ClientWriteError || !result.Committed ||
		result.BytesWritten != 2 || result.FramesWritten != 0 || result.ProtocolComplete || len(connection.writes) != 1 {
		t.Fatalf("short write was accepted or retried: %+v, writes=%d", result, len(connection.writes))
	}
}

func TestExecuteRoundKeepsUnknownWriteCommit(t *testing.T) {
	connection := &fakeConnection{frames: []Frame{{Data: []byte("request")}}, writeErr: errors.New("unknown write"), zeroWrite: true}
	result := Execute(context.Background(), Round{Connection: connection, Handle: func(context.Context, Frame) ([]Frame, bool, error) {
		return []Frame{{Data: []byte("response")}}, true, nil
	}})
	if result.Termination != ClientWriteError || !result.Committed || result.ProtocolComplete || result.FramesWritten != 0 || result.BytesWritten != 0 {
		t.Fatalf("unknown write was treated as safely uncommitted: %+v", result)
	}
}

func TestExecuteRoundRejectsCompletionAfterHandlerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connection := &fakeConnection{frames: []Frame{{Data: []byte("request")}}}
	result := Execute(ctx, Round{Connection: connection, Handle: func(context.Context, Frame) ([]Frame, bool, error) {
		cancel()
		return []Frame{{Data: []byte("must not write")}}, true, nil
	}})
	if result.Termination != Canceled || result.ProtocolComplete || result.Committed || len(connection.writes) != 0 || result.Dispatch != Dispatching {
		t.Fatalf("canceled handler was allowed to finish: %+v", result)
	}
}

func TestExecuteRoundCancellationAndInvalidRoundAreBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := &fakeConnection{frames: []Frame{{Data: []byte("request")}}}
	result := Execute(ctx, Round{Connection: connection, Handle: func(context.Context, Frame) ([]Frame, bool, error) {
		t.Fatal("canceled round invoked handler")
		return nil, false, nil
	}})
	if result.Termination != Canceled || result.Dispatch != NotDispatched || result.Committed {
		t.Fatalf("canceled round escaped its context: %+v", result)
	}
	if result := Execute(context.Background(), Round{}); result.Termination != Failed || result.Dispatch != NotDispatched {
		t.Fatalf("invalid round was dispatched: %+v", result)
	}
}
