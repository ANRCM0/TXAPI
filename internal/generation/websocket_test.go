package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	bridge "minigateway/internal/foundation/runtime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
)

// This fixture is a real socket consumer, not a product wire protocol. Only
// trusted bindings cross into Runner; application frames never carry authority.
type socketCommand struct {
	Round    string
	Recorded bool
	Body     json.RawMessage
}

type socketEvent struct {
	Kind     string
	Round    string
	RecordID string
	Status   int
	Metadata execv1.ResponseMetadata
}

type socketOutput struct {
	conn  *transport.Conn
	round string
	state execv1.OutputState
}

func (o *socketOutput) Commit(ctx context.Context, metadata execv1.ResponseMetadata) error {
	if o.state.Committed {
		return errors.New("fixture output already committed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.state.Committed = true // An ambiguous write must remain committed.
	return socketSend(ctx, o.conn, socketEvent{Kind: "metadata", Round: o.round, Metadata: metadata})
}
func (o *socketOutput) Write(ctx context.Context, data []byte) (int, error) {
	if !o.state.Committed {
		return 0, errors.New("fixture output not committed")
	}
	if err := o.conn.Write(ctx, transport.Binary, data); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (o *socketOutput) Flush(ctx context.Context) error { return ctx.Err() }
func (o *socketOutput) State() execv1.OutputState       { return o.state }

func socketSend(ctx context.Context, c *transport.Conn, event socketEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return c.Write(ctx, transport.Text, data)
}

type socketHarness struct {
	server    *httptest.Server
	manager   *transport.Manager
	capacity  *access.ConnectionCapacity
	runner    *Runner
	sessions  sync.WaitGroup
	active    atomic.Int64
	completed chan execv1.Outcome
}

func newSocketHarness(t *testing.T, st *store.Store, runner *Runner) *socketHarness {
	t.Helper()
	manager, err := transport.New(context.Background(), transport.Options{Connections: 4, MaxMessageBytes: 1 << 20,
		HandshakeTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, MaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := access.NewConnectionCapacity(access.ConnectionLimits{Instance: 4, User: 2, Key: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := &socketHarness{manager: manager, capacity: capacity, runner: runner, completed: make(chan execv1.Outcome, 8)}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		key := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		view, ok := st.LookupToken(key)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		binding := access.Binding{UserID: view.UserID, TokenID: view.ID}
		release, err := capacity.Acquire(req.Context(), binding)
		if err != nil {
			http.Error(w, "connection limit", 429)
			return
		}
		conn, err := manager.Accept(w, req)
		if err != nil {
			release()
			return
		}
		h.sessions.Add(1)
		h.active.Add(1)
		go func() {
			defer h.sessions.Done()
			defer h.active.Add(-1)
			defer release()
			defer conn.Close()
			h.serve(conn, binding)
		}()
	}))
	t.Cleanup(func() {
		capacity.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
		// Join HTTP handlers before waiting on their session registrations, then
		// join Runner before application goroutines and the fixture's store close.
		h.server.Close()
		closeRunner(t, runner)
		h.sessions.Wait()
	})
	return h
}

func (h *socketHarness) serve(conn *transport.Conn, binding access.Binding) {
	var jobs sync.WaitGroup
	defer jobs.Wait()
	var busy atomic.Bool
	for {
		typ, data, err := conn.Read(conn.Context())
		if err != nil {
			return
		}
		var cmd socketCommand
		if typ != transport.Text || json.Unmarshal(data, &cmd) != nil || len(cmd.Round) > 64 || !busy.CompareAndSwap(false, true) {
			conn.Close()
			return
		}
		ctx := access.WithRequestID(conn.Context(), cmd.Round)
		in := execv1.Invocation{Operation: protocol.Chat, Body: cmd.Body}
		var f *Future
		var id string
		if cmd.Recorded {
			f, id, err = h.runner.SubmitRecorded(ctx, binding, in)
		} else {
			f, err = h.runner.Submit(ctx, binding, &socketOutput{conn: conn, round: cmd.Round}, in)
		}
		if err != nil {
			busy.Store(false)
			_ = socketSend(conn.Context(), conn, socketEvent{Kind: "refused", Round: cmd.Round, Status: 503})
			continue
		}
		if cmd.Recorded {
			_ = socketSend(conn.Context(), conn, socketEvent{Kind: "accepted", Round: cmd.Round, RecordID: id})
		}
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			waitCtx := context.Background()
			stop := func() bool { return false }
			if cmd.Recorded {
				waitCtx = conn.Context() // Observer loss never cancels recorded work.
			} else {
				stop = context.AfterFunc(conn.Context(), f.Cancel)
			}
			result, err := f.Wait(waitCtx)
			stop()
			busy.Store(false)
			if err == nil || result.StatusCode != 0 {
				select {
				case h.completed <- result:
				default:
				}
				_ = socketSend(conn.Context(), conn, socketEvent{Kind: "done", Round: cmd.Round, Status: result.StatusCode})
			}
		}()
	}
}

func socketKey(t *testing.T, st *store.Store, owner uint) *model.Token {
	t.Helper()
	key, err := st.IssueToken(owner, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (h *socketHarness) dial(t *testing.T, key *model.Token) *ws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http"), &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key.Key}}})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func sendRound(t *testing.T, conn *ws.Conn, round string, in execv1.Invocation, recorded bool) {
	t.Helper()
	data, err := json.Marshal(socketCommand{Round: round, Body: in.Body, Recorded: recorded})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, ws.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readSocketUntil(t *testing.T, conn *ws.Conn, kind string) (socketEvent, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var body []byte
	for range 32 {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == ws.MessageBinary {
			body = append(body, data...)
			continue
		}
		var event socketEvent
		if json.Unmarshal(data, &event) != nil {
			t.Fatal("invalid fixture event")
		}
		if event.Kind == kind {
			return event, body
		}
	}
	t.Fatal("fixture event bound exceeded")
	return socketEvent{}, nil
}

func socketEventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("socket fixture did not release ownership")
}

func TestWebSocketIdleCapacityAndPerRoundAuthority(t *testing.T) {
	for _, revoke := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoked-key", false: "narrowed-policy"}[revoke], func(t *testing.T) {
			st, binding, _ := fixture(t)
			key, idleKey := socketKey(t, st, binding.UserID), socketKey(t, st, binding.UserID)
			engine, ch := strictRecordingFixture(t, st)
			var hits atomic.Int64
			g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
				Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
			h := newSocketHarness(t, st, newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute))
			conn := h.dial(t, key)
			_ = h.dial(t, idleKey)
			socketEventually(t, func() bool { return h.capacity.Snapshot().Active == 2 })
			if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 || hits.Load() != 0 {
				t.Fatalf("idle sockets consumed execution slots: %+v", got)
			}
			var count int64
			if err := st.DB().Model(&model.BillingReservation{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("idle sockets reserved budget: %d %v", count, err)
			}
			_, resp, err := ws.Dial(context.Background(), "ws"+strings.TrimPrefix(h.server.URL, "http"), &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key.Key}}})
			if err == nil || resp == nil || resp.StatusCode != 429 {
				t.Fatalf("identity connection limit bypassed: %v", err)
			}
			in := strictSocketExecution()
			sendRound(t, conn, "same-external-id", in, false)
			first, body := readSocketUntil(t, conn, "done")
			if first.Status != 200 || len(body) == 0 || hits.Load() != 1 {
				t.Fatalf("first round: %+v hits=%d", first, hits.Load())
			}
			input := &store.TokenInput{Status: model.TokenStatusDisabled, ModelMode: access.All}
			if !revoke {
				input = &store.TokenInput{Status: model.TokenStatusActive, ModelMode: access.Allowlist, Models: []string{}}
			}
			if _, err := st.UpdateOwnedToken(binding.UserID, key.ID, input); err != nil {
				t.Fatal(err)
			}
			sendRound(t, conn, "same-external-id", in, false)
			second, _ := readSocketUntil(t, conn, "done")
			want := 403
			if revoke {
				want = 401
			}
			if second.Status != want || hits.Load() != 1 || h.manager.Snapshot().Active != 2 {
				t.Fatalf("round reused stale authority or lost socket: %+v hits=%d", second, hits.Load())
			}
			if err := st.DB().Model(&model.BillingReservation{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("denied round touched ledger: %d %v", count, err)
			}
		})
	}
}

func strictSocketExecution() execv1.Invocation {
	return execv1.Invocation{Operation: protocol.Chat, Body: []byte(`{"model":"m","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)}
}

func TestWebSocketForegroundDisconnectPreservesUnknownStrictCost(t *testing.T) {
	st, binding, _ := fixture(t)
	key := socketKey(t, st, binding.UserID)
	engine, ch := strictRecordingFixture(t, st)
	started := make(chan struct{})
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict, RetryTimes: 2,
		Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			hits.Add(1)
			close(started)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}})
	h := newSocketHarness(t, st, newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute))
	conn := h.dial(t, key)
	sendRound(t, conn, "disconnect", strictSocketExecution(), false)
	signal(t, started)
	_ = conn.CloseNow()
	socketEventually(t, func() bool { return h.active.Load() == 0 })
	select {
	case result := <-h.completed:
		if result.BillingState != billing.Pending || len(result.Attempts) != 1 || result.Phase != execv1.Dispatching || result.ProtocolComplete || result.HTTPStatus != 0 {
			t.Fatalf("disconnect lost unknown execution facts: %+v", result)
		}
	default:
		t.Fatal("foreground worker was not joined")
	}
	assertArchiveHolds(t, st)
	if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 || h.capacity.Snapshot().Active != 0 || hits.Load() != 1 {
		t.Fatalf("disconnect leaked slots or retried: %+v hits=%d", got, hits.Load())
	}
}

func TestWebSocketRecordedObserverDisconnectDoesNotCancelGeneration(t *testing.T) {
	st, binding, _ := fixture(t)
	key := socketKey(t, st, binding.UserID)
	binding.TokenID = key.ID
	engine, ch := strictRecordingFixture(t, st)
	a := testArchive(t, archiveOptions(st))
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
		Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			hits.Add(1)
			close(started)
			select {
			case <-release:
				return response(), nil
			case <-req.Context().Done():
				close(canceled)
				return nil, req.Context().Err()
			}
		})}})
	h := newSocketHarness(t, st, recordingRunner(t, st, a, bridge.NewExecutor(g).Execute))
	conn := h.dial(t, key)
	sendRound(t, conn, "recorded", strictSocketExecution(), true)
	accepted, _ := readSocketUntil(t, conn, "accepted")
	signal(t, started)
	if len(accepted.RecordID) != 64 {
		t.Fatal("recorded handoff did not expose its owned record")
	}
	_ = conn.CloseNow()
	socketEventually(t, func() bool { return h.active.Load() == 0 })
	if err := h.manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
		t.Fatal("observer/transport shutdown canceled accepted recorded work")
	default:
	}
	if capacity.Snapshot().Requests != 1 || h.capacity.Snapshot().Active != 0 {
		t.Fatal("connection and recorded execution lifetimes were conflated")
	}
	close(release)
	var recording Recording
	socketEventually(t, func() bool {
		var err error
		recording, err = a.Read(context.Background(), binding, accepted.RecordID)
		return err == nil
	})
	if recording.Result.BillingState != billing.Settled || recording.Result.StatusCode != 200 || len(recording.Body) == 0 || hits.Load() != 1 {
		t.Fatalf("recorded generation was lost: %+v", recording)
	}
	socketEventually(t, func() bool { return capacity.Snapshot().Requests == 0 && capacity.Snapshot().Attempts == 0 })
}

func TestWebSocketConcurrentSameRoundIDKeepsOperationOutputIsolated(t *testing.T) {
	st, binding, _ := fixture(t)
	key1, key2 := socketKey(t, st, binding.UserID), socketKey(t, st, binding.UserID)
	var hits atomic.Int64
	bothStarted := make(chan struct{})
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if hits.Add(1) == 2 {
			close(bothStarted)
		}
		select {
		case <-bothStarted:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		body, _ := io.ReadAll(req.Body)
		marker := "first"
		if bytes.Contains(body, []byte("second")) {
			marker = "second"
		}
		wire := `{"choices":[{"message":{"content":"` + marker + `"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})}})
	r, err := New(context.Background(), Options{Workers: 2, QueueCapacity: 2, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second,
		Resolve: st.ResolvePrincipal, Execute: bridge.NewExecutor(g).Execute})
	if err != nil {
		t.Fatal(err)
	}
	h := newSocketHarness(t, st, r)
	c1, c2 := h.dial(t, key1), h.dial(t, key2)
	first, second := execution(), execution()
	second.Body = bytes.ReplaceAll(second.Body, []byte("hello"), []byte("second"))
	sendRound(t, c1, "same", first, false)
	sendRound(t, c2, "same", second, false)
	e1, b1 := readSocketUntil(t, c1, "done")
	e2, b2 := readSocketUntil(t, c2, "done")
	if e1.Status != 200 || e2.Status != 200 || !bytes.Contains(b1, []byte("first")) || bytes.Contains(b1, []byte("second")) ||
		!bytes.Contains(b2, []byte("second")) || bytes.Contains(b2, []byte("first")) || hits.Load() != 2 {
		t.Fatalf("round state crossed connections: %s / %s; %+v %+v", b1, b2, e1, e2)
	}
}
