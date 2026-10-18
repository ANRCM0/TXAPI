package websocket_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	compat "minigateway/internal/compat/websocket"
	"minigateway/internal/config"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
)

func socketPair(t *testing.T) (context.Context, *ws.Conn, v2.Connection, *transport.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	m, err := transport.New(ctx, transport.Options{Connections: 1, MaxMessageBytes: 1024, HandshakeTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, MaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	accepted := make(chan *transport.Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- c
	}))
	t.Cleanup(s.Close)
	client, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })
	select {
	case c := <-accepted:
		return ctx, client, compat.Adapt(v2.ConnectionID(t.Name()), c), m
	case <-ctx.Done():
		t.Fatal("accept timed out")
		return nil, nil, nil, nil
	}
}

// The payloads below are a test protocol, not a product-native WebSocket API.
func TestRealSocketSerialRoundsKeepConnectionAndLeaseRevision(t *testing.T) {
	ctx, client, connection, manager := socketPair(t)
	leases, err := gateway.NewRuntimeLeaseSet("r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	first, err := leases.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Release() })
	finished := make(chan v2.RoundResult, 1)
	go func() {
		result := (gateway.WebSocketRoundExecutor{}).ExecuteRound(ctx, v2.Round{Connection: connection, ID: 1, Handle: func(ctx context.Context, frame v2.Frame) ([]v2.Frame, bool, error) {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
			return []v2.Frame{{Type: frame.Type, Data: []byte(first.Revision() + ":" + string(frame.Data))}}, true, nil
		}})
		_ = first.Release()
		finished <- result
	}()
	if err := client.Write(ctx, ws.MessageText, []byte("one")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("round did not enter handler")
	}
	if err := leases.Publish("r2"); err != nil {
		t.Fatal(err)
	}
	if first.Revision() != "r1" || leases.Current() != "r2" {
		t.Fatal("published revision mutated active lease")
	}
	if err := leases.Publish("r1"); err == nil {
		t.Fatal("live retiring revision lost its lease")
	}
	close(resume)
	assertResponse(t, ctx, client, "r1:one")
	assertRound(t, ctx, finished)
	if manager.Snapshot().Active != 1 {
		t.Fatal("round completion closed physical connection")
	}
	second, err := leases.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.Revision() != "r2" {
		t.Fatal("next round acquired stale generation")
	}
	// A previously retiring name is reusable only after its last lease drains.
	if err := leases.Publish("r1"); err != nil {
		t.Fatalf("completed round leaked r1: %v", err)
	}
	go func() {
		finished <- (gateway.WebSocketRoundExecutor{}).ExecuteRound(ctx, v2.Round{Connection: connection, ID: 2, Handle: func(_ context.Context, frame v2.Frame) ([]v2.Frame, bool, error) {
			return []v2.Frame{{Type: frame.Type, Data: []byte(second.Revision() + ":" + string(frame.Data))}}, true, nil
		}})
	}()
	if err := client.Write(ctx, ws.MessageText, []byte("two")); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, ctx, client, "r2:two")
	assertRound(t, ctx, finished)
	if manager.Snapshot().Active != 1 {
		t.Fatal("second round closed physical connection")
	}
	for i := 0; i < 2; i++ {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if manager.Snapshot() != (transport.Snapshot{}) {
		t.Fatal("repeated close leaked transport capacity")
	}
}

func TestRealSocketRoundOwnerRefreshesSQLiteAuthorityBeforeDispatch(t *testing.T) {
	ctx, client, connection, manager := socketPair(t)
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "authority.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "ws-user", Password: "example-password-123", ModelMode: access.Allowlist, Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "ws-key", Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	trusted, _, ok := identityimpl.NewTokenAuthenticator(st).Authenticate(ctx, key.Key)
	if !ok {
		t.Fatal("authentication failed")
	}
	authority := identityimpl.New(st.ResolvePrincipal)
	binding := identity.Binding{UserID: user.ID, TokenID: key.ID}
	var calls atomic.Int64
	// The application owner refreshes identity before entering mechanical execution.
	run := func() (v2.RoundResult, error) {
		current, _, err := authority.Refresh(trusted, binding)
		if err != nil {
			return v2.RoundResult{}, err
		}
		return (gateway.WebSocketRoundExecutor{}).ExecuteRound(current, v2.Round{Connection: connection, Handle: func(_ context.Context, frame v2.Frame) ([]v2.Frame, bool, error) {
			calls.Add(1)
			return []v2.Frame{frame}, true, nil
		}}), nil
	}
	finished := make(chan v2.RoundResult, 1)
	go func() {
		result, err := run()
		if err != nil {
			t.Error(err)
		}
		finished <- result
	}()
	if err := client.Write(ctx, ws.MessageText, []byte("authorized")); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, ctx, client, "authorized")
	assertRound(t, ctx, finished)
	if calls.Load() != 1 {
		t.Fatal("first round skipped upstream callback")
	}
	if err := st.DeleteOwnedToken(user.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := run(); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("revoked key entered next round: %v", err)
	}
	if calls.Load() != 1 || manager.Snapshot().Active != 1 {
		t.Fatal("revoked round dispatched or closed owner connection")
	}
}

func assertResponse(t *testing.T, ctx context.Context, client *ws.Conn, want string) {
	t.Helper()
	typ, data, err := client.Read(ctx)
	if err != nil || typ != ws.MessageText || string(data) != want {
		t.Fatalf("response = %q type=%v error=%v, want %q", data, typ, err, want)
	}
}

func assertRound(t *testing.T, ctx context.Context, done <-chan v2.RoundResult) {
	t.Helper()
	select {
	case result := <-done:
		if result.Termination != v2.Success || result.Dispatch != v2.ResponseReceived || !result.ProtocolComplete || !result.Committed || result.FramesRead != 1 || result.FramesWritten != 1 {
			t.Fatalf("round facts = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("round did not complete")
	}
}
