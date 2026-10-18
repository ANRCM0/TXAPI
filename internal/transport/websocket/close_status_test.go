package websocket

import (
	"context"
	"errors"
	ws "github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func statusPair(t *testing.T) (*Manager, *Conn, *ws.Conn) {
	t.Helper()
	manager := testManager(t, testOptions())
	accepted := make(chan *Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := manager.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	t.Cleanup(server.Close)
	peer := dial(t, server.URL, nil)
	return manager, receiveConn(t, accepted), peer
}
func TestCloseStatusSendsOnlyBoundedCodeAndJoinsWorker(t *testing.T) {
	manager, server, peer := statusPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.CloseStatus(ctx, 1005); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- server.CloseStatus(ctx, 1008) }()
	_, _, err := peer.Read(ctx)
	if ws.CloseStatus(err) != ws.StatusPolicyViolation {
		t.Fatalf("unexpected close code: %v", err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("close worker did not finish")
	}
	if snapshot := manager.Snapshot(); snapshot.Active != 0 || snapshot.Pending != 0 {
		t.Fatal(snapshot)
	}
}
func TestCloseStatusTimeoutRetainsCapacityAndShutdownOwnership(t *testing.T) {
	manager, server, peer := statusPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := server.CloseStatus(ctx, 1001); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	if err := server.CloseStatus(context.Background(), 1001); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if snapshot := manager.Snapshot(); snapshot.Pending != 1 {
		t.Fatalf("lost close worker: %+v", snapshot)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	_ = peer.CloseNow()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.Snapshot(); snapshot.Active != 0 || snapshot.Pending != 0 {
		t.Fatal(snapshot)
	}
}
