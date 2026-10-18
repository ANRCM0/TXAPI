package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagerDialSharesCapacityAndOwnsLifetime(t *testing.T) {
	serverManager := testManager(t, testOptions())
	accepted := make(chan *Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			http.Error(w, "no auth", 401)
			return
		}
		c, err := serverManager.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer server.Close()
	opts := testOptions()
	opts.Connections = 1
	clientManager := testManager(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := clientManager.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Authorization": {"Bearer fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	peer := receiveConn(t, accepted)
	cancel()
	if err := c.Write(context.Background(), Text, []byte("still alive")); err != nil {
		t.Fatal(err)
	}
	_, data, err := peer.Read(context.Background())
	if err != nil || string(data) != "still alive" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err := clientManager.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	if got := clientManager.Snapshot(); got.Active != 0 || got.Pending != 0 {
		t.Fatal(got)
	}
}
func TestDialNoRedirectAndSanitizedFailures(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.Error(w, "provider-secret", 500) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	m := testManager(t, testOptions())
	for _, url := range []string{"ws" + strings.TrimPrefix(redirect.URL, "http"), "ws://user:secret@localhost/a", "ws://localhost/a?token=secret", "ws://localhost/a#secret", "https://localhost"} {
		if _, err := m.Dial(context.Background(), url, nil); err != ErrHandshake {
			t.Fatalf("unsanitized result: %v", err)
		}
		if got := m.Snapshot(); got.Active != 0 || got.Pending != 0 {
			t.Fatal(got)
		}
	}
	if hits != 0 {
		t.Fatal("followed redirect")
	}
}
func TestDialCloseCancelsPendingHandshake(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	m := testManager(t, testOptions())
	finished := make(chan error, 1)
	go func() {
		_, err := m.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handshake not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("handshake succeeded")
		}
	case <-ctx.Done():
		t.Fatal("dial not joined")
	}
	if got := m.Snapshot(); got.Active != 0 || got.Pending != 0 {
		t.Fatal(got)
	}
}
