package generation

import (
	"bytes"
	"context"
	"errors"
	"io"
	bridge "minigateway/internal/foundation/runtime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

type memoryOutput struct {
	body     bytes.Buffer
	metadata execv1.ResponseMetadata
	state    execv1.OutputState
	flush    func()
}

func (o *memoryOutput) Commit(ctx context.Context, metadata execv1.ResponseMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.metadata, o.state.Committed = metadata, true
	return nil
}
func (o *memoryOutput) Write(ctx context.Context, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if o.body.Len()+len(data) > 1<<20 {
		return 0, errors.New("fixture output limit")
	}
	return o.body.Write(data)
}
func (o *memoryOutput) Flush(ctx context.Context) error {
	if o.flush != nil {
		o.flush()
	}
	return ctx.Err()
}
func (o *memoryOutput) State() execv1.OutputState { return o.state }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}
}

func execution() execv1.Invocation {
	return execv1.Invocation{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)}
}

func fixture(t *testing.T) (*store.Store, access.Binding, access.Binding) {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "generation.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	user, err := st.CreateUser(&store.UserInput{Username: "generation-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key1, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key2, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	return st, access.Binding{UserID: user.ID, TokenID: key1.ID}, access.Binding{UserID: user.ID, TokenID: key2.ID}
}

func channels() []model.Channel {
	return []model.Channel{{ID: 1, Name: "isolated-fixture", Type: "openai", BaseURL: "http://upstream.invalid", Models: model.StringList{"m"},
		Status: model.StatusEnabled, Weight: 1, Credentials: []model.Credential{{ID: 1, APIKey: "fixture-provider-secret", Status: model.CredentialActive, Weight: 1}}}}
}

func newGateway(t *testing.T, opts gateway.Options) (*gateway.Gateway, *access.Capacity) {
	t.Helper()
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 4, User: 2, Key: 1, Channel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Channels == nil {
		opts.Channels = channels()
	}
	opts.Admission = gateway.CapacityAdmission{Capacity: capacity}
	opts.TotalTimeout = 5 * time.Second
	opts.Protocols, opts.Operations = protocol.Protocols(nil), protocol.Operations()
	g, err := gateway.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close(); capacity.Close() })
	return g, capacity
}

func newRunner(t *testing.T, resolve func(context.Context, access.Binding) (access.Principal, error), execute func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome) *Runner {
	t.Helper()
	r, err := New(context.Background(), Options{Workers: 1, QueueCapacity: 2, MaxRequestBytes: 4096, MaxDuration: 5 * time.Second, Resolve: resolve, Execute: execute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRunner(t, r) })
	return r
}

func closeRunner(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Error(err)
	}
}

func submit(t *testing.T, r *Runner, binding access.Binding, out *memoryOutput) *Future {
	t.Helper()
	f, err := r.Submit(context.Background(), binding, out, execution())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func wait(t *testing.T, f *Future) (execv1.Outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.Wait(ctx)
}

func signal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for generation fixture")
	}
}

func TestWorkerReauthorizesQueuedBindingBeforeGatewayAdmission(t *testing.T) {
	for _, revoke := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoked-key", false: "narrowed-policy"}[revoke], func(t *testing.T) {
			st, binding, queuedBinding := fixture(t)
			if revoke {
				queuedBinding = binding
			}
			started, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int64
			g, capacity := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				if hits.Add(1) == 1 {
					close(started)
					select {
					case <-release:
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				}
				return response(), nil
			})}})
			r := newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute)
			first := submit(t, r, binding, &memoryOutput{})
			signal(t, started)
			output := &memoryOutput{}
			queued := submit(t, r, queuedBinding, output)
			changes := map[string]any{"status": model.TokenStatusDisabled}
			if !revoke {
				changes = map[string]any{"model_mode": access.Allowlist, "models": model.StringList{}}
			}
			if err := st.DB().Model(&model.Token{}).Where("id = ?", queuedBinding.TokenID).Updates(changes).Error; err != nil {
				t.Fatal(err)
			}
			close(release)
			if result, err := wait(t, first); err != nil || result.StatusCode != 200 {
				t.Fatalf("active generation changed by later revocation: %+v %v", result, err)
			}
			result, err := wait(t, queued)
			want := 403
			if revoke {
				want = 401
				if !errors.Is(err, access.ErrUnauthorized) || output.state.Committed {
					t.Fatalf("revoked binding touched output: %+v %v", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if result.StatusCode != want || result.Phase != execv1.NotDispatched || len(result.Attempts) != 0 || hits.Load() != 1 {
				t.Fatalf("stale queued authority escaped: %+v %v hits=%d", result, err, hits.Load())
			}
			if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 {
				t.Fatalf("queued refusal leaked capacity: %+v", got)
			}
		})
	}
}

func TestObserverDisconnectDoesNotCancelHandoffOrRetainItsContext(t *testing.T) {
	st, binding, _ := fixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	g, _ := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return response(), nil
	})}})
	r := newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute)
	first := submit(t, r, binding, &memoryOutput{})
	signal(t, started)
	observer, disconnect := context.WithCancel(context.Background())
	observer = access.WithPrincipal(observer, access.Principal{UserID: 900, TokenID: 901, Policy: access.Policy{Mode: access.Allowlist}})
	observer = access.WithRequestID(observer, "worker-fixture")
	observer = access.WithRateAdmission(observer, binding.TokenID)
	observer = access.WithTokenReservation(observer, 999, 100)
	in := execution()
	out := &memoryOutput{}
	queued, err := r.Submit(observer, binding, out, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Body[0] = '!'
	disconnect()
	if _, err := queued.Wait(observer); !errors.Is(err, context.Canceled) {
		t.Fatalf("observation did not cancel independently: %v", err)
	}
	close(release)
	if result, err := wait(t, first); err != nil || result.StatusCode != 200 {
		t.Fatalf("first execution: %+v %v", result, err)
	}
	result, err := wait(t, queued)
	if err != nil || result.StatusCode != 200 || result.Identity.UserID != binding.UserID || result.Identity.TokenID != binding.TokenID ||
		result.Identity.RequestID != "worker-fixture" || !result.Committed || result.HTTPStatus != 0 || hits.Load() != 2 {
		t.Fatalf("handoff retained observer state or body: %+v %v", result, err)
	}
	if _, err := wait(t, queued); !errors.Is(err, ErrResultConsumed) {
		t.Fatalf("result pointers can be delivered to multiple owners: %v", err)
	}
}

func TestWorkerQueueBoundCancelAndCloseReleaseSharedCapacity(t *testing.T) {
	st, binding, _ := fixture(t)
	started := make(chan struct{})
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			close(started)
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}})
	r := newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute)
	active := submit(t, r, binding, &memoryOutput{})
	signal(t, started)
	queued := submit(t, r, binding, &memoryOutput{})
	other := submit(t, r, binding, &memoryOutput{})
	if _, err := r.Submit(context.Background(), binding, &memoryOutput{}, execution()); !errors.Is(err, ErrFull) {
		t.Fatalf("queue admitted unbounded work: %v", err)
	}
	if got := capacity.Snapshot(); got.Requests != 1 || got.Attempts != 1 {
		t.Fatalf("queued work held network/generation capacity: %+v", got)
	}
	queued.Cancel()
	queued.Cancel()
	closeRunner(t, r)
	if result, err := wait(t, active); err != nil || len(result.Attempts) != 1 || result.Attempts[0].Termination != execv1.Canceled {
		t.Fatalf("active cancellation bypassed coordinator: %+v %v", result, err)
	}
	for _, f := range []*Future{queued, other} {
		if result, err := wait(t, f); !errors.Is(err, context.Canceled) || result.Phase != execv1.NotDispatched {
			t.Fatalf("queued cancellation dispatched: %+v %v", result, err)
		}
	}
	if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 || hits.Load() != 1 {
		t.Fatalf("close failed to drain: %+v hits=%d", got, hits.Load())
	}
	if _, err := r.Submit(context.Background(), binding, &memoryOutput{}, execution()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed runner accepted work: %v", err)
	}
	// The shared gateway/capacity remain usable after this consumer is closed.
	principal, err := st.ResolvePrincipal(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := capacity.AcquireRequest(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	lease()
}

func TestWorkerPanicKeepsCommitFactsAndWorkerSurvives(t *testing.T) {
	st, binding, _ := fixture(t)
	var hits atomic.Int64
	g, capacity := newGateway(t, gateway.Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		hits.Add(1)
		if strings.Contains(req.Header.Get("Accept"), "event-stream") {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))}, nil
		}
		return response(), nil
	})}})
	r := newRunner(t, st.ResolvePrincipal, bridge.NewExecutor(g).Execute)
	out := &memoryOutput{flush: func() { panic("secret panic detail must not escape") }}
	in := execution()
	in.Body = []byte(`{"model":"m","stream":true}`)
	f, err := r.Submit(context.Background(), binding, out, in)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wait(t, f)
	if !errors.Is(err, ErrExecutionPanic) || !result.Committed || result.HTTPStatus != 0 || result.Replayable || result.Phase != execv1.Dispatching || hits.Load() != 1 {
		t.Fatalf("panic lost commit/unknown facts: %+v %v hits=%d", result, err, hits.Load())
	}
	if result, err := wait(t, submit(t, r, binding, &memoryOutput{})); err != nil || result.StatusCode != 200 || hits.Load() != 2 {
		t.Fatalf("panic killed worker or leaked leases: %+v %v", result, err)
	}
	if got := capacity.Snapshot(); got.Requests != 0 || got.Attempts != 0 {
		t.Fatalf("panic leaked capacity: %+v", got)
	}
}

func TestWorkerAuthorityFailsClosedWithoutOutputOrSensitiveErrors(t *testing.T) {
	for _, kind := range []string{"error", "wrong-owner", "invalid-policy", "panic"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int64
			r := newRunner(t, func(context.Context, access.Binding) (access.Principal, error) {
				switch kind {
				case "error":
					return access.Principal{}, errors.New("database secret details")
				case "panic":
					panic("private panic details")
				case "wrong-owner":
					return access.Principal{UserID: 99, TokenID: 1, Policy: access.Policy{Mode: access.All}}, nil
				default:
					return access.Principal{UserID: 1, TokenID: 1}, nil
				}
			}, func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
				calls.Add(1)
				return execv1.Outcome{}
			})
			out := &memoryOutput{}
			result, err := wait(t, submit(t, r, access.Binding{UserID: 1, TokenID: 1}, out))
			want := ErrAuthorityUnavailable
			if kind == "panic" {
				want = ErrExecutionPanic
			}
			if !errors.Is(err, want) || calls.Load() != 0 || out.state.Committed || out.body.Len() != 0 || result.Phase != execv1.NotDispatched {
				t.Fatalf("unsafe authority fallback: %+v %v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestRunnerShutdownTimeoutKeepsOwnershipUntilCallbacksReturn(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := newRunner(t, func(context.Context, access.Binding) (access.Principal, error) {
		close(started)
		<-release // Simulate a trusted callback that ignores cancellation.
		return access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}}, nil
	}, func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
		panic("canceled authorization dispatched")
	})
	f := submit(t, r, access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{})
	signal(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close pretended callback was joined: %v", err)
	}
	close(release)
	closeRunner(t, r)
	if result, err := wait(t, f); !errors.Is(err, context.Canceled) || result.Phase != execv1.NotDispatched {
		t.Fatalf("shutdown race dispatched a canceled generation: %+v %v", result, err)
	}
}

func TestRunnerConcurrentSubmitAndCloseCompleteEveryAcceptedFuture(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	r, err := New(root, Options{Workers: 3, QueueCapacity: 16, MaxRequestBytes: 4096, MaxDuration: time.Second,
		Resolve: func(context.Context, access.Binding) (access.Principal, error) {
			return access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}}, nil
		},
		Execute: func(ctx context.Context, _ execv1.Output, _ execv1.Invocation) execv1.Outcome {
			<-ctx.Done()
			return execv1.Outcome{Result: execv1.Result{Termination: execv1.Canceled}}
		}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := r.Submit(context.Background(), access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{}, execution())
			if err == nil {
				_, err = wait(t, f)
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			} else if !errors.Is(err, ErrClosed) && !errors.Is(err, ErrFull) {
				t.Error(err)
			}
		}()
	}
	cancel()
	closeRunner(t, r)
	wg.Wait()
}

func TestRunnerDeadlineBoundsAuthorizationAndExecution(t *testing.T) {
	for _, blockAuthority := range []bool{true, false} {
		t.Run(map[bool]string{true: "authority", false: "execution"}[blockAuthority], func(t *testing.T) {
			var executed atomic.Bool
			r, err := New(context.Background(), Options{Workers: 1, QueueCapacity: 1, MaxRequestBytes: 4096, MaxDuration: 50 * time.Millisecond,
				Resolve: func(ctx context.Context, _ access.Binding) (access.Principal, error) {
					if blockAuthority {
						<-ctx.Done()
						return access.Principal{}, ctx.Err()
					}
					return access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}}, nil
				},
				Execute: func(ctx context.Context, _ execv1.Output, _ execv1.Invocation) execv1.Outcome {
					executed.Store(true)
					<-ctx.Done()
					return execv1.Outcome{Result: execv1.Result{Termination: execv1.Deadline}}
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRunner(t, r)
			result, err := wait(t, submit(t, r, access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{}))
			if blockAuthority && (!errors.Is(err, context.DeadlineExceeded) || executed.Load() || result.Phase != execv1.NotDispatched || result.Termination != execv1.Deadline) {
				t.Fatalf("authority exceeded dispatch deadline: %+v %v", result, err)
			}
			if !blockAuthority && (err != nil || !executed.Load() || result.Termination != execv1.Deadline) {
				t.Fatalf("execution lost generation deadline: %+v %v", result, err)
			}
		})
	}
}

func TestRunnerRejectsUnboundedConfigurationAndHandoff(t *testing.T) {
	opts := Options{Workers: 1, QueueCapacity: 1, MaxRequestBytes: 4096, MaxDuration: time.Second,
		Resolve: func(context.Context, access.Binding) (access.Principal, error) { panic("rejected handoff authorized") },
		Execute: func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
			panic("rejected handoff executed")
		}}
	for _, modify := range []func(*Options){
		func(o *Options) { o.Resolve = nil },
		func(o *Options) { o.Execute = nil },
		func(o *Options) { o.Workers = 0 },
		func(o *Options) { o.QueueCapacity = 0 },
		func(o *Options) { o.MaxDuration = 0 },
		func(o *Options) { o.Workers = 1024; o.QueueCapacity = 4096; o.MaxRequestBytes = 20 << 20 },
	} {
		bad := opts
		modify(&bad)
		if r, err := New(context.Background(), bad); err == nil {
			closeRunner(t, r)
			t.Fatal("unbounded or incomplete runner configuration accepted")
		}
	}
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRunner(t, r)
	for _, in := range []execv1.Invocation{{},
		{Operation: protocol.Chat, Body: make([]byte, 4097)},
		{Operation: protocol.Chat, Body: []byte(`{}`), SessionID: strings.Repeat("x", 257)},
		{Operation: protocol.Operation("unknown"), Body: []byte(`{}`)},
	} {
		if _, err := r.Submit(context.Background(), access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{}, in); err == nil {
			t.Fatal("unbounded handoff accepted")
		}
	}
	if _, err := r.Submit(context.Background(), access.Binding{}, &memoryOutput{}, execution()); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("missing trusted binding accepted: %v", err)
	}
}

func TestQueueResidenceUsesAcceptanceDeadlineAndDoesNotDispatchExpiredWork(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var resolves, executions atomic.Int64
	r, err := New(context.Background(), Options{Workers: 1, QueueCapacity: 1, MaxRequestBytes: 4096, MaxDuration: 50 * time.Millisecond,
		Resolve: func(ctx context.Context, _ access.Binding) (access.Principal, error) {
			resolves.Add(1)
			close(started)
			<-release
			return access.Principal{}, ctx.Err()
		},
		Execute: func(context.Context, execv1.Output, execv1.Invocation) execv1.Outcome {
			executions.Add(1)
			return execv1.Outcome{}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRunner(t, r)
	first := submit(t, r, access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{})
	signal(t, started)
	queued := submit(t, r, access.Binding{UserID: 1, TokenID: 1}, &memoryOutput{})
	// Both acceptance deadlines have elapsed before the blocked worker proceeds.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	close(release)
	for _, f := range []*Future{first, queued} {
		result, err := wait(t, f)
		if !errors.Is(err, context.DeadlineExceeded) || result.Phase != execv1.NotDispatched || result.Termination != execv1.Deadline {
			t.Fatalf("expired queue entry received a fresh execution lifetime: %+v %v", result, err)
		}
	}
	if resolves.Load() != 1 || executions.Load() != 0 {
		t.Fatalf("expired queued work reached authority or executor: resolves=%d executions=%d", resolves.Load(), executions.Load())
	}
}
