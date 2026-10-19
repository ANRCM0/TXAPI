package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	codec "minigateway/internal/compat/responsesws"
	"minigateway/internal/config"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type nativeFixtureConnection struct {
	mu       sync.Mutex
	frames   []v2.Frame
	requests [][]byte
	block    <-chan struct{}
	entered  chan struct{}
	once     sync.Once
	closed   atomic.Bool
}

func (c *nativeFixtureConnection) ID() v2.ConnectionID {
	return v2.ConnectionID(fmt.Sprintf("native-fixture-%p", c))
}
func (c *nativeFixtureConnection) Write(_ context.Context, frame v2.Frame) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return 0, errors.New("closed fixture")
	}
	c.requests = append(c.requests, append([]byte(nil), frame.Data...))
	id := fmt.Sprintf("resp-%d", len(c.requests))
	for _, event := range []struct{ typ, status string }{{"response.created", "in_progress"}, {"response.completed", "completed"}} {
		response := map[string]any{"id": id, "object": "response", "model": "actual", "status": event.status, "output": []any{}}
		if event.status == "completed" {
			response["usage"] = map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}
		}
		data, _ := json.Marshal(map[string]any{"type": event.typ, "stream_id": "main", "response": response})
		c.frames = append(c.frames, v2.Frame{Type: 1, Data: data})
	}
	return len(frame.Data), nil
}
func (c *nativeFixtureConnection) Read(ctx context.Context) (v2.Frame, error) {
	c.mu.Lock()
	if len(c.frames) == 0 {
		c.mu.Unlock()
		return v2.Frame{}, errors.New("fixture exhausted")
	}
	frame := c.frames[0]
	last := len(c.frames) == 1
	c.frames = c.frames[1:]
	c.mu.Unlock()
	if last && c.block != nil {
		c.once.Do(func() { close(c.entered) })
		select {
		case <-c.block:
		case <-ctx.Done():
			return v2.Frame{}, ctx.Err()
		}
	}
	return frame, nil
}
func (c *nativeFixtureConnection) Close() error { c.closed.Store(true); return nil }
func (c *nativeFixtureConnection) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

type nativeFixtureResource struct {
	dials  atomic.Int64
	conn   *nativeFixtureConnection
	closed chan struct{}
	once   sync.Once
	key    string
}

func (r *nativeFixtureResource) Dial(_ context.Context, key string) (v2.Connection, error) {
	r.dials.Add(1)
	if key != r.key {
		return nil, errors.New("wrong credential binding")
	}
	return r.conn, nil
}
func (r *nativeFixtureResource) Close(context.Context) error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

type nativeServiceFixture struct {
	service   *NativeWebSocketService
	store     *store.Store
	trusted   context.Context
	binding   identity.Binding
	channels  []model.Channel
	resources []*nativeFixtureResource
	block     <-chan struct{}
}

func newNativeServiceFixture(t *testing.T) *nativeServiceFixture {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "native-service.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	user := fixtureOwner(t, st)
	key, err := st.IssueToken(user, &store.TokenInput{ModelMode: identity.All, RPMLimit: 20, TPMLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	trusted, _, ok := authority.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
	if !ok {
		t.Fatal("fixture authentication failed")
	}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Admission: CapacityAdmission{Capacity: capacity}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	f := &nativeServiceFixture{store: st, trusted: trusted, binding: identity.Binding{UserID: user, TokenID: key.ID}}
	f.channels = []model.Channel{{ID: 1, Name: "official", Type: "openai", BaseURL: "https://api.openai.com", Models: model.StringList{"alias"}, ModelMapping: model.StringMap{"alias": "actual"}, Weight: 1, Status: model.StatusEnabled, Credentials: []model.Credential{{ID: 7, ChannelID: 1, APIKey: "fixture-old-key", HealthGeneration: 1, Status: model.CredentialActive, Weight: 1}}}}
	f.service, err = NewNativeWebSocketService(context.Background(), g, NativeWSOptions{Authority: authority.New(st.ResolvePrincipal), Gate: access.NewRateGate(access.NewMemoryLimiter()), Models: []string{"actual"}, Limits: contract.Limits{MaxMessageBytes: 256 << 10, IdleTimeout: time.Second, RoundTimeout: 5 * time.Second, MaxLifetime: time.Minute},
		Prepare: func(context.Context) (NativeWSResource, error) {
			key := f.channels[0].Credentials[0].APIKey
			r := &nativeFixtureResource{conn: &nativeFixtureConnection{block: f.block, entered: make(chan struct{})}, closed: make(chan struct{}), key: key}
			f.resources = append(f.resources, r)
			return r, nil
		},
		Parse: func(frame v2.Frame) (NativeWSCreate, error) {
			r, err := codec.Parse(frame)
			return NativeWSCreate{Model: r.Model, PreviousID: r.PreviousID, PromptText: r.PromptText, Initial: r.Initial}, err
		}, Encode: codec.MapModel, Decoder: func(model string) v2.FrameDecoder { return codec.NewDecoder(model).Decode }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *nativeServiceFixture) publish(t *testing.T) {
	t.Helper()
	if err := f.service.Publish(context.Background(), f.channels); err != nil {
		t.Fatal(err)
	}
}
func (f *nativeServiceFixture) open(t *testing.T, id string) contract.Session {
	t.Helper()
	s, p := f.service.Open(f.trusted, contract.Open{Binding: f.binding, ConnectionID: v2.ConnectionID(id)})
	if p != nil {
		t.Fatalf("open: %s", p.Code)
	}
	return s
}
func nativeCreate(previous string) contract.Create {
	m := map[string]any{"type": "response.create", "model": "alias", "input": "hello"}
	if previous != "" {
		m["previous_response_id"] = previous
	}
	b, _ := json.Marshal(m)
	return contract.Create{JSON: b}
}
func nativeDiscard(_ context.Context, f v2.Frame) (int, error) { return len(f.Data), nil }

func TestNativeSessionManagedGrantRejectsBeforeDialAndKeepsOtherKeysIndependent(t *testing.T) {
	for _, state := range []string{"active", "disabled channel", "disabled credential"} {
		t.Run(state, func(t *testing.T) {
			f := newNativeServiceFixture(t)
			managed := managedTestChannels()[1]
			managed.Models = model.StringList{"alias"}
			managed.Credentials[0].BindingOwnerID = f.binding.UserID
			managed.Credentials[0].BindingTokenID = f.binding.TokenID
			managed.Credentials[0].BindingModel = "alias"
			if state == "disabled channel" {
				managed.Status = model.StatusDisabled
			}
			if state == "disabled credential" {
				managed.Credentials[0].Status = model.CredentialDisabled
			}
			f.channels = append(f.channels, managed)
			f.publish(t)
			s := f.open(t, "managed-bound")
			result := s.Execute(f.trusted, nativeCreate(""), nativeDiscard)
			if result.Problem == nil || result.Facts.UpstreamWriteStarted || result.Facts.Dispatch != v2.NotDispatched || f.resources[0].dials.Load() != 0 || f.resources[0].conn.count() != 0 {
				t.Fatalf("managed grant used static WS transport: %+v dials=%d", result, f.resources[0].dials.Load())
			}
			key, err := f.store.IssueToken(f.binding.UserID, &store.TokenInput{ModelMode: identity.All})
			if err != nil {
				t.Fatal(err)
			}
			trusted, _, ok := authority.NewTokenAuthenticator(f.store).Authenticate(context.Background(), key.Key)
			if !ok {
				t.Fatal("independent key authentication failed")
			}
			other, problem := f.service.Open(trusted, contract.Open{Binding: identity.Binding{UserID: f.binding.UserID, TokenID: key.ID}, ConnectionID: "static-independent"})
			if problem != nil {
				t.Fatalf("independent key open: %s", problem.Code)
			}
			result = other.Execute(trusted, nativeCreate(""), nativeDiscard)
			if result.Problem != nil || result.Facts.Termination != v2.Success || f.resources[0].dials.Load() != 1 || f.resources[0].conn.count() != 1 {
				t.Fatalf("managed grant disturbed independent static WS: %+v", result)
			}
		})
	}
}

func TestNativeSessionContinuationPinsOneConnectionAndCurrentIdentity(t *testing.T) {
	f := newNativeServiceFixture(t)
	f.publish(t)
	s := f.open(t, "downstream-1")
	if _, p := f.service.Open(context.Background(), contract.Open{Binding: f.binding, ConnectionID: "forged"}); p == nil || p.Code != "unauthorized" {
		t.Fatal("constructed binding authenticated")
	}
	first := s.Execute(f.trusted, nativeCreate(""), nativeDiscard)
	if first.Problem != nil || first.Facts.Termination != v2.Success {
		t.Fatalf("first: %+v", first)
	}
	second := s.Execute(f.trusted, nativeCreate("resp-1"), nativeDiscard)
	if second.Problem != nil || second.Facts.Termination != v2.Success {
		t.Fatalf("continuation: %+v", second)
	}
	c := f.resources[0].conn
	if c.count() != 2 {
		t.Fatal("continuation redialed or replayed")
	}
	var mapped map[string]any
	_ = json.Unmarshal(c.requests[0], &mapped)
	if mapped["model"] != "actual" {
		t.Fatal("model mapping not applied")
	}
	if err := f.store.DeleteOwnedToken(f.binding.UserID, f.binding.TokenID); err != nil {
		t.Fatal(err)
	}
	rejected := s.Execute(f.trusted, nativeCreate("resp-2"), nativeDiscard)
	if rejected.Problem == nil || rejected.Problem.Code != "unauthorized" || rejected.Facts.UpstreamWriteStarted || c.count() != 2 {
		t.Fatal("revoked round dispatched")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.service.capacity.Snapshot().Active != 0 {
		t.Fatal("connection lease leaked")
	}
}
func TestNativeSessionRejectsForeignOrOldHandleBeforeDispatch(t *testing.T) {
	for _, previous := range []string{"foreign", "resp-1"} {
		t.Run(previous, func(t *testing.T) {
			f := newNativeServiceFixture(t)
			f.publish(t)
			s := f.open(t, "handle-owner")
			if r := s.Execute(f.trusted, nativeCreate(""), nativeDiscard); r.Problem != nil {
				t.Fatal(r.Problem.Code)
			}
			if previous == "resp-1" {
				if r := s.Execute(f.trusted, nativeCreate("resp-1"), nativeDiscard); r.Problem != nil {
					t.Fatal(r.Problem.Code)
				}
			}
			before := f.resources[0].conn.count()
			r := s.Execute(f.trusted, nativeCreate(previous), nativeDiscard)
			if r.Problem == nil || r.Problem.Code != "continuation_invalid" || r.Facts.UpstreamWriteStarted || f.resources[0].conn.count() != before {
				t.Fatal("foreign/old handle dispatched")
			}
		})
	}
}
func TestNativeSessionBusyPublicationAndDrainJoin(t *testing.T) {
	f := newNativeServiceFixture(t)
	finish := make(chan struct{})
	f.block = finish
	f.publish(t)
	s := f.open(t, "live-old")
	result := make(chan contract.Result, 1)
	go func() { result <- s.Execute(f.trusted, nativeCreate(""), nativeDiscard) }()
	resourceWait(t, f.resources[0].conn.entered)
	if r := s.Execute(f.trusted, nativeCreate(""), nativeDiscard); r.Problem == nil || r.Problem.Code != "busy" {
		t.Fatal("overlap not rejected")
	}
	old := f.resources[0]
	f.channels[0].Credentials[0].APIKey = "fixture-new-key"
	f.channels[0].Credentials[0].HealthGeneration++
	f.block = nil
	f.publish(t)
	select {
	case <-old.closed:
		t.Fatal("old active generation closed")
	default:
	}
	close(finish)
	if r := <-result; r.Problem != nil {
		t.Fatal(r.Problem.Code)
	}
	r := s.Execute(f.trusted, nativeCreate("resp-1"), nativeDiscard)
	if r.Problem == nil || r.Problem.Code != "continuation_invalid" || old.conn.count() != 1 {
		t.Fatal("old session crossed resource revision")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, old.closed)
	fresh := f.open(t, "live-new")
	if r := fresh.Execute(f.trusted, nativeCreate(""), nativeDiscard); r.Problem != nil {
		t.Fatal(r.Problem.Code)
	}
	if err := f.service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.service.capacity.Snapshot().Active != 0 {
		t.Fatal("drain leaked session capacity")
	}
}

type nativeRateObservation struct {
	requests  int
	estimates []int
	delta     int
	inherited bool
	token     uint
}

func (l *nativeRateObservation) AllowRequest(ctx context.Context, _ string, _ int, _ time.Time) (bool, int) {
	l.requests++
	l.inherited = l.inherited || access.HasRateAdmission(ctx, l.token)
	return true, 0
}
func (l *nativeRateObservation) AllowTokens(_ context.Context, _ string, _ int, estimate int, _ time.Time) (bool, int) {
	l.estimates = append(l.estimates, estimate)
	return true, 0
}
func (l *nativeRateObservation) AddTokens(_ context.Context, _ string, delta int, _ time.Time) {
	l.delta += delta
}

func TestNativeSessionReservesEachRoundAndAccountsContinuationUsage(t *testing.T) {
	f := newNativeServiceFixture(t)
	limiter := &nativeRateObservation{token: f.binding.TokenID}
	f.service.options.Gate = access.NewRateGate(limiter)
	f.publish(t)
	s := f.open(t, "rate-rounds")
	parent := access.WithRateAdmission(access.WithTokenReservation(f.trusted, f.binding.TokenID, 99), f.binding.TokenID)
	for _, previous := range []string{"", "resp-1"} {
		if result := s.Execute(parent, nativeCreate(previous), nativeDiscard); result.Problem != nil {
			t.Fatal(result.Problem.Code)
		}
	}
	if limiter.requests != 2 || len(limiter.estimates) != 2 || limiter.inherited || limiter.estimates[1] != limiter.estimates[0]+3 {
		t.Fatalf("round admission or history estimate changed: %+v", limiter)
	}
	if got := limiter.estimates[0] + limiter.estimates[1] + limiter.delta; got != 6 {
		t.Fatalf("completed rounds charged %d tokens, want 6", got)
	}
	before := limiter.delta
	access.FinishTokens(parent, limiter, 98)
	if limiter.delta != before-1 {
		t.Fatal("round consumed its parent's mutable reservation")
	}
}

func TestNativeSessionRefundsKnownNonDispatchButRetainsUncertainUsage(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			f := newNativeServiceFixture(t)
			limiter := &nativeRateObservation{token: f.binding.TokenID}
			f.service.options.Gate = access.NewRateGate(limiter)
			if uncertain {
				f.publish(t)
			}
			s := f.open(t, "rate-failure")
			if uncertain {
				f.service.options.Decoder = func(string) v2.FrameDecoder {
					return func(v2.Frame) (v2.Envelope, error) { return v2.Envelope{}, errors.New("unparseable upstream") }
				}
			}
			r := s.Execute(f.trusted, nativeCreate(""), nativeDiscard)
			if r.Problem == nil || r.Facts.UpstreamWriteStarted != uncertain || len(limiter.estimates) != 1 {
				t.Fatalf("wrong dispatch observation: %+v", r)
			}
			charged := limiter.estimates[0] + limiter.delta
			if (!uncertain && charged != 0) || (uncertain && charged != limiter.estimates[0]) {
				t.Fatalf("uncertain=%v charged=%d estimate=%d", uncertain, charged, limiter.estimates[0])
			}
		})
	}
}
