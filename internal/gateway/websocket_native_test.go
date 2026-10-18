package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	codec "minigateway/internal/compat/responsesws"
	bridge "minigateway/internal/compat/websocket"
	"minigateway/internal/config"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
)

type nativeTestResource struct {
	manager  *transport.Manager
	endpoint string
	revision string
	serial   atomic.Uint64
	closed   chan struct{}
	once     sync.Once
}

func (r *nativeTestResource) Dial(ctx context.Context) (v2.Connection, error) {
	c, err := r.manager.Dial(ctx, r.endpoint, http.Header{"Authorization": {"Bearer server-only-fixture"}})
	if err != nil {
		return nil, err
	}
	return bridge.Adapt(v2.ConnectionID(fmt.Sprintf("%s-%d", r.revision, r.serial.Add(1))), c), nil
}
func (r *nativeTestResource) Close(ctx context.Context) error {
	if err := r.manager.Close(ctx); err != nil {
		return err
	}
	r.once.Do(func() { close(r.closed) })
	return nil
}
func nativeManager(t *testing.T) *transport.Manager {
	t.Helper()
	m, err := transport.New(context.Background(), transport.Options{Connections: 8, MaxMessageBytes: 256 << 10, HandshakeTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, MaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func nativeJSON(value any) []byte { b, _ := json.Marshal(value); return b }
func nativeEvent(typ, status string) []byte {
	return nativeJSON(map[string]any{"type": typ, "stream_id": "main", "response": map[string]any{"id": "response-fixture", "object": "response", "model": "m", "status": status, "output": []any{}, "usage": map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}})
}

func TestNativeWebSocketHostRealTransportRevocationAndResourcePublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "native.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "native-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	auth := authority.NewTokenAuthenticator(st)
	trusted, _, ok := auth.Authenticate(ctx, key.Key)
	if !ok {
		t.Fatal("fixture authentication failed")
	}
	binding := identity.Binding{UserID: user.ID, TokenID: key.ID}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	upstreamManager := nativeManager(t)
	accepted := make(chan struct{}, 1)
	unblock := make(chan struct{})
	var attempts, httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			httpCalls.Add(1)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer server-only-fixture" {
			w.WriteHeader(403)
			return
		}
		c, err := upstreamManager.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		typ, body, err := c.Read(ctx)
		if err != nil {
			return
		}
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		if typ != transport.Text || request["type"] != "response.create" || request["store"] != false || request["stream_id"] != "main" {
			return
		}
		number := attempts.Add(1)
		if err := c.Write(ctx, transport.Text, nativeEvent("response.created", "in_progress")); err != nil {
			return
		}
		if number == 1 {
			accepted <- struct{}{}
			select {
			case <-unblock:
			case <-ctx.Done():
				return
			}
		}
		_ = c.Write(ctx, transport.Text, nativeEvent("response.completed", "completed"))
	}))
	defer upstream.Close()
	resources := newWebSocketResources()
	makeResource := func(revision string) *nativeTestResource {
		return &nativeTestResource{manager: nativeManager(t), endpoint: "ws" + strings.TrimPrefix(upstream.URL, "http"), revision: revision, closed: make(chan struct{})}
	}
	first := makeResource("r1")
	if err := resources.prepare(ctx, "r1", func(context.Context) (websocketResource, error) { return first, nil }); err != nil {
		t.Fatal(err)
	}
	if err := resources.publish("r1", nil); err != nil {
		t.Fatal(err)
	}
	host := websocketHost{authority: authority.New(st.ResolvePrincipal), gate: access.NewRateGate(access.NewMemoryLimiter()), admission: CapacityAdmission{Capacity: capacity}, resources: resources,
		parse: func(frame v2.Frame) (websocketCreate, error) {
			r, err := codec.Parse(frame)
			return websocketCreate{model: r.Model, previous: r.PreviousID, initial: r.Initial}, err
		},
		decoder: func(model string) v2.FrameDecoder { return codec.NewDecoder(model).Decode }, billingDisabled: func() bool { return true }, allowed: func(model string) bool { return model == "m" }}
	downstreamManager := nativeManager(t)
	results := make(chan v2.ExchangeResult, 4)
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticated, _, ok := auth.Authenticate(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !ok {
			w.WriteHeader(401)
			return
		}
		c, err := downstreamManager.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			typ, body, err := c.Read(ctx)
			if err != nil {
				return
			}
			result := host.execute(authenticated, binding, v2.Frame{Type: uint8(typ), Data: body}, func(ctx context.Context, frame v2.Frame) (int, error) {
				err := c.Write(ctx, transport.MessageType(frame.Type), frame.Data)
				if err != nil {
					return 0, err
				}
				return len(frame.Data), nil
			})
			results <- result
			if result.Err != nil {
				return
			}
		}
	}))
	defer downstream.Close()
	client, err := nativeManager(t).Dial(ctx, "ws"+strings.TrimPrefix(downstream.URL, "http"), http.Header{"Authorization": {"Bearer " + key.Key}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := nativeJSON(map[string]any{"type": "response.create", "model": "m", "input": "hello"})
	if err := client.Write(ctx, transport.Text, request); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, accepted)
	if _, body, err := client.Read(ctx); err != nil || !strings.Contains(string(body), "response.created") {
		t.Fatalf("created: %s %v", body, err)
	}
	second := makeResource("r2")
	if err := resources.prepare(ctx, "r2", func(context.Context) (websocketResource, error) { return second, nil }); err != nil {
		t.Fatal(err)
	}
	if err := resources.publish("r2", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.closed:
		t.Fatal("retired transport closed while upstream exchange active")
	default:
	}
	close(unblock)
	if _, body, err := client.Read(ctx); err != nil || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("terminal: %s %v", body, err)
	}
	result := <-results
	if result.Termination != v2.Success || !result.UpstreamWriteStarted || !result.DeliveryCommitted || !result.ProtocolComplete || result.Usage.Completeness != v2.Complete {
		t.Fatalf("native exchange: %+v", result)
	}
	resourceWait(t, first.closed)
	if err := client.Write(ctx, transport.Text, request); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := client.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if result := <-results; result.Termination != v2.Success {
		t.Fatalf("next revision failed: %+v", result)
	}
	if err := st.DeleteOwnedToken(user.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.Write(ctx, transport.Text, request); err != nil {
		t.Fatal(err)
	}
	result = <-results
	if result.Dispatch != v2.NotDispatched || result.UpstreamWriteStarted || attempts.Load() != 2 || httpCalls.Load() != 0 {
		t.Fatalf("revoked round dispatched or HTTP fallback: %+v attempts=%d HTTP=%d", result, attempts.Load(), httpCalls.Load())
	}
	if err := resources.drain(ctx); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, second.closed)
	if _, _, err := host.authority.Refresh(trusted, binding); err == nil {
		t.Fatal("real revoked identity accepted")
	}
}
