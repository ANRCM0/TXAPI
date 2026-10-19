package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	execution "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	usage "minigateway/internal/foundation/contracts/usagehistory/v1"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/gateway"
	"minigateway/internal/management"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/server"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
	"minigateway/internal/usageconsole"
)

// This is a test-only process composition. The same production assembly, store,
// codecs and socket adapters run in the child. Only the existing resource and
// persistence ports receive loopback routing and deterministic crash barriers.
type wsCrashSettings struct {
	Database    config.DatabaseConfig
	ContentKeys string
	Upstream    string
	Ready       string
	Marker      string
	Stage       string
}

func (s wsCrashSettings) checkpoint(stage string) {
	if s.Stage != stage {
		return
	}
	if err := os.WriteFile(s.Marker, []byte(stage), 0600); err != nil {
		panic(err)
	}
	// Deliberately retain this exact instruction boundary until the parent
	// kills the process. No production timeout or cancellation is substituted.
	select {}
}

type wsCrashResource struct {
	*wsAssemblyResource
	settings wsCrashSettings
}

func (r *wsCrashResource) Dial(ctx context.Context, key string) (execution.Connection, error) {
	r.settings.checkpoint("before-send")
	connection, err := r.wsAssemblyResource.Dial(ctx, key)
	if err != nil {
		return nil, err
	}
	return &wsCrashConnection{Connection: connection, settings: r.settings}, nil
}

type wsCrashConnection struct {
	execution.Connection
	settings wsCrashSettings
}

func (c *wsCrashConnection) Read(ctx context.Context) (execution.Frame, error) {
	c.settings.checkpoint("before-id")
	return c.Connection.Read(ctx)
}

type wsCrashHistory struct {
	history.Repository
	settings wsCrashSettings
}

func (h wsCrashHistory) Apply(ctx context.Context, binding identity.Binding, changes []state.HistoryChange, guards ...state.HistoryGuard) error {
	if err := h.Repository.Apply(ctx, binding, changes, guards...); err != nil {
		return err
	}
	for _, change := range changes {
		if change.Record.Version == 2 && change.Record.Busy == "" {
			h.settings.checkpoint("before-delivery")
		}
	}
	return nil
}

type wsCrashUsage struct {
	usage.Recorder
	settings wsCrashSettings
}

func (r wsCrashUsage) Finish(ctx context.Context, completion usage.Completion) error {
	if completion.Host.ProtocolComplete && completion.Host.ProtocolStatus == "completed" && completion.Host.DeliveryCommitted {
		r.settings.checkpoint("before-usage-finish")
	}
	return r.Recorder.Finish(ctx, completion)
}

func TestResponsesWSHistoryCrashProcess(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_WS_CRASH_FILE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	var settings wsCrashSettings
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &settings) != nil {
		t.Fatal("invalid subprocess settings", err)
	}
	endpoint, err := url.Parse(settings.Upstream)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "" || endpoint.User != nil {
		t.Fatal("subprocess upstream must be the explicit loopback fixture")
	}
	st, err := store.Open(settings.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// This is the same recovery order as main: metadata first, then the
	// Responses runtime recovers history before any HTTP or WS producer starts.
	if err := st.InterruptUsageHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	limiter := access.NewMemoryLimiter()
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(), BillingMode: billing.ModeDisabled,
		Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: capacity},
		UsageHistory: wsCrashUsage{Recorder: st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 8, OwnerRecords: 8, RetentionSeconds: 3600}), settings: settings},
		Client: &http.Client{Transport: generationTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("subprocess forbids HTTP inference")
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	local, err := startResponsesRuntime(responsesRuntimeConfig(settings.ContentKeys), st, gw)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	native, err := startResponsesWSRuntimeWithFactory(config.ResponsesWSConfig{Enabled: true, PersistentHistory: true, Models: []string{"m"}}, st, gw, limiter,
		func(root context.Context, limits contract.Limits) (gateway.NativeWSResource, error) {
			manager, err := transport.New(root, transport.Options{Connections: 8, MaxMessageBytes: limits.MaxMessageBytes, HandshakeTimeout: 2 * time.Second, ReadTimeout: limits.RoundTimeout, WriteTimeout: 2 * time.Second, MaxLifetime: limits.MaxLifetime})
			if err != nil {
				return nil, err
			}
			resource := &wsAssemblyResource{manager: manager, endpoint: "ws" + strings.TrimPrefix(settings.Upstream, "http"), prefix: 1, closed: make(chan struct{})}
			return &wsCrashResource{wsAssemblyResource: resource, settings: settings}, nil
		}, wsCrashHistory{Repository: local.History(), settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer joinResponsesWSRuntime(native)
	control := controlplane.New(persistence.Control(st))
	configuration := management.NewConfiguration(persistence.Configuration(st), func() error {
		channels, err := st.ChannelSnapshot()
		if err != nil {
			return err
		}
		return native.Publish(channels)
	}, nil)
	engine := server.NewEngine(server.Deps{Configuration: configuration, Generation: gateway.NewHTTPExecutor(gw), TokenAuth: authority.NewTokenAuthenticator(st), Control: control,
		Metrics: gw.Metrics(), Responses: local.Handler(), ResponsesWS: native.Handler(),
		UsageHistory: usageconsole.New(st.UsageHistory(true, 3600), control, limiter), RateLimiter: limiter, RateGate: access.NewRateGate(limiter),
		AdminAuth: middleware.AdminAuth("crash-admin-fixture")})
	front := httptest.NewServer(engine)
	defer front.Close()
	if err := os.WriteFile(settings.Ready, []byte(front.URL), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}

type wsCrashChild struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	output  string
	marker  string
	url     string
	ctx     context.Context
}

func startWSCrashChild(t *testing.T, ctx context.Context, settings wsCrashSettings) *wsCrashChild {
	t.Helper()
	dir := t.TempDir()
	settings.Ready, settings.Marker = filepath.Join(dir, "ready"), filepath.Join(dir, "checkpoint")
	path := filepath.Join(dir, "child.json")
	if err := os.WriteFile(path, wsAssemblyJSON(settings), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "child-output")
	file, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestResponsesWSHistoryCrashProcess$", "-test.count=1", "-test.timeout=90s")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TXAPI_TEST_WS_CRASH_FILE=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "TXAPI_TEST_WS_CRASH_FILE="+path)
	command.Stdout, command.Stderr = file, file
	if err := command.Start(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	child := &wsCrashChild{command: command, done: make(chan struct{}), output: output, marker: settings.Marker, ctx: ctx}
	go func() {
		child.err = command.Wait()
		_ = file.Close()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(10 * time.Second):
			t.Error("child did not exit after Kill")
		}
	})
	child.wait(t, "ready", func() bool {
		raw, err := os.ReadFile(settings.Ready)
		if err != nil {
			return false
		}
		parsed, err := url.Parse(string(raw))
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
			return false
		}
		client := http.Client{Timeout: time.Second}
		response, err := client.Get(parsed.String() + "/healthz")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			return false
		}
		child.url = parsed.String()
		return true
	})
	return child
}

func (p *wsCrashChild) wait(t *testing.T, label string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !predicate() {
		select {
		case <-p.done:
			output, _ := os.ReadFile(p.output)
			t.Fatalf("child exited before %s: %v\n%s", label, p.err, output)
		case <-p.ctx.Done():
			t.Fatal("parent test lifetime expired", p.ctx.Err())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %s timeout", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *wsCrashChild) checkpoint(t *testing.T, stage string) {
	t.Helper()
	marker := p.marker
	p.wait(t, stage, func() bool { raw, _ := os.ReadFile(marker); return string(raw) == stage })
}

func (p *wsCrashChild) kill(t *testing.T) {
	t.Helper()
	if err := p.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
		if p.err == nil {
			t.Fatal("child exited gracefully at a crash boundary")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not join after Kill")
	}
}

func (p *wsCrashChild) dial(t *testing.T, key string) *ws.Conn {
	t.Helper()
	connection, _, err := ws.Dial(p.ctx, "ws"+strings.TrimPrefix(p.url, "http")+"/v1/responses", &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection
}

func (p *wsCrashChild) get(t *testing.T, path, key string, status int) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(p.ctx, "GET", p.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil || response.StatusCode != status {
		t.Fatalf("child GET %s: status=%d body=%s error=%v", path, response.StatusCode, body, err)
	}
	return body
}

func wsCrashFinancialImage(t *testing.T, st *store.Store) []byte {
	t.Helper()
	var users []model.User
	var keys []model.Token
	var balances []model.BillingBalance
	var reservations, attempts int64
	for _, query := range []func() error{
		func() error { return st.DB().Select("id, quota, used_quota").Order("id").Find(&users).Error },
		func() error { return st.DB().Select("id, quota, used_quota").Order("id").Find(&keys).Error },
		func() error { return st.DB().Order("owner_type, owner_id").Find(&balances).Error },
		func() error { return st.DB().Model(&model.BillingReservation{}).Count(&reservations).Error },
		func() error { return st.DB().Model(&model.BillingAttempt{}).Count(&attempts).Error },
	} {
		if err := query(); err != nil {
			t.Fatal(err)
		}
	}
	return wsAssemblyJSON([]any{users, keys, balances, reservations, attempts})
}

func TestResponsesWSHistoryProcessKillDurableBoundaries(t *testing.T) {
	for _, stage := range []string{"before-send", "before-id", "before-delivery", "before-usage-finish"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			f := &wsAssemblyFixture{t: t, ctx: ctx, cancel: cancel, persistentHistory: true, observations: make(chan wsAssemblyObservation, 16), blocked: make(chan struct{}, 4), release: make(chan struct{})}
			t.Cleanup(f.close)
			var err error
			f.upstreamManager, err = transport.New(ctx, transport.Options{Connections: 8, MaxMessageBytes: 256 << 10, HandshakeTimeout: time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: time.Second, MaxLifetime: 2 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			f.upstream = httptest.NewServer(http.HandlerFunc(f.upstreamHandler))
			settings := wsCrashSettings{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "history-crash.db"), CredentialKeysFile: runtimeKeyFile(t, "crash-credentials", 101, 102)},
				ContentKeys: runtimeKeyFile(t, "crash-history", 103, 104), Upstream: f.upstream.URL, Stage: stage}
			st, err := store.Open(settings.Database)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if st != nil {
					_ = st.Close()
				}
			})
			owner, err := st.CreateUser(&store.UserInput{Username: "crash-owner", Password: "fixture-password-123", ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			key, err := st.IssueToken(owner.ID, &store.TokenInput{ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.CreateChannel(&store.ChannelInput{Name: "crash-official", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"alias"}, ModelMapping: map[string]string{"alias": "m"}, APIKey: "crash-upstream-fixture", Weight: 1, Status: "enabled"})
			if err != nil {
				t.Fatal(err)
			}
			session, _, err := st.Login("crash-owner", "fixture-password-123")
			if err != nil {
				t.Fatal(err)
			}
			f.key = key.Key
			money := wsCrashFinancialImage(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			child := startWSCrashChild(t, ctx, settings)
			connection := child.dial(t, key.Key)
			sendStoredCreate(t, f, connection, "", "crash-root", false)
			child.checkpoint(t, stage)
			expected := int64(1)
			if stage == "before-send" {
				expected = 0
			} else {
				child.wait(t, "actual upstream receipt", func() bool { return f.attempts.Load() == 1 })
			}
			var delivered bool
			var publicID string
			if stage == "before-usage-finish" {
				// A successful socket Write is not proof that an observer has
				// consumed the frame. Observe it before killing the writer process;
				// an abrupt TCP reset can discard data that the peer has not read.
				for !delivered {
					read, stop := context.WithTimeout(ctx, 3*time.Second)
					_, raw, err := connection.Read(read)
					stop()
					if err != nil {
						t.Fatal("terminal not received before crash", err)
					}
					var event struct {
						Type     string
						Response struct{ ID string }
					}
					if json.Unmarshal(raw, &event) != nil || event.Type == "error" {
						t.Fatalf("unexpected pre-crash event: %s", raw)
					}
					if event.Response.ID != "" {
						publicID = event.Response.ID
					}
					delivered = event.Type == "response.completed"
				}
			}
			child.kill(t)
			for {
				read, stop := context.WithTimeout(ctx, 2*time.Second)
				_, raw, err := connection.Read(read)
				stop()
				if err != nil {
					break
				}
				var event struct {
					Type     string
					Response struct{ ID string }
				}
				if json.Unmarshal(raw, &event) != nil {
					t.Fatal("invalid pre-crash public event")
				}
				if event.Response.ID != "" {
					publicID = event.Response.ID
				}
				delivered = delivered || event.Type == "response.completed"
			}
			if delivered != (stage == "before-usage-finish") || f.attempts.Load() != expected {
				t.Fatal("crash boundary did not match terminal delivery and real sends", delivered, f.attempts.Load())
			}
			st, err = store.Open(settings.Database)
			if err != nil {
				t.Fatal(err)
			}
			var histories []model.ResponseHistory
			var invocations []model.UsageInvocation
			if st.DB().Find(&histories).Error != nil || len(histories) != 1 || st.DB().Find(&invocations).Error != nil || len(invocations) != 1 {
				t.Fatal("crash did not leave exactly one durable invocation/history")
			}
			old, invocation := histories[0], invocations[0]
			known := stage == "before-delivery" || stage == "before-usage-finish"
			if publicID != "" && publicID != old.ID || (old.Busy == "") != known || invocation.State != "open" {
				t.Fatal("history and usage crash facts were conflated")
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			settings.Stage = ""
			recovered := startWSCrashChild(t, ctx, settings)
			want := 409
			if known {
				want = 200
			}
			recovered.get(t, "/v1/responses/"+old.ID, key.Key, want)
			var detail struct{ Data usage.Detail }
			body := recovered.get(t, "/api/me/usage-records/"+invocation.ID, session, 200)
			if json.Unmarshal(body, &detail) != nil || detail.Data.Record.State != "interrupted" || len(detail.Data.Attempts) != 1 {
				t.Fatal("recovery duplicated or lost usage invocation", string(body))
			}
			attempt := detail.Data.Attempts[0]
			if attempt.MayDispatch != (expected == 1) {
				t.Fatal("recovery invented or lost dispatch evidence")
			}
			if stage == "before-usage-finish" {
				if attempt.State != "observed" || attempt.Host.ProtocolStatus != "completed" || attempt.Usage.TotalTokens == nil || *attempt.Usage.TotalTokens != "3" {
					t.Fatal("known usage was lost behind interrupted host state", string(body))
				}
			} else if attempt.Usage.TotalTokens != nil {
				t.Fatal("unknown interrupted usage was filled with a count", string(body))
			}
			next := recovered.dial(t, key.Key)
			parent := old.ID
			if !known {
				sendStoredCreate(t, f, next, old.ID, "must-not-replay", false)
				f.storedRefusal(t, next)
				if f.attempts.Load() != expected {
					t.Fatal("unknown parent triggered an upstream replay")
				}
				parent = ""
				next = recovered.dial(t, key.Key)
			}
			sendStoredCreate(t, f, next, parent, "new-after-recovery", false)
			fresh := ""
			for fresh == "" {
				_, raw, err := next.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var event struct {
					Type     string
					Response struct{ ID, Model string }
				}
				_ = json.Unmarshal(raw, &event)
				if event.Type == "error" {
					t.Fatalf("new authorized generation failed: %s", raw)
				}
				if event.Type == "response.completed" {
					fresh = event.Response.ID
					if fresh == old.ID || event.Response.Model != "alias" {
						t.Fatal("new generation reused the old result")
					}
				}
			}
			recovered.get(t, "/v1/responses/"+fresh, key.Key, 200)
			recovered.wait(t, "new usage seal", func() bool {
				var page usage.Page
				raw := recovered.get(t, "/api/me/usage-records", session, 200)
				if json.Unmarshal(raw, &page) != nil || len(page.Data) != 2 {
					return false
				}
				for _, row := range page.Data {
					if row.ID != invocation.ID && row.State == "sealed" {
						return true
					}
				}
				return false
			})
			if f.attempts.Load() != expected+1 {
				t.Fatal("recovery dispatched more than one new generation")
			}
			if expected == 1 {
				historyWire(t, <-f.observations, 1)
			}
			items := 1
			if known {
				items = 3
			}
			historyWire(t, <-f.observations, items)
			recovered.kill(t)
			// Repeat startup recovery without another generation. Both the
			// immutable old history and the new result must retain identity.
			again := startWSCrashChild(t, ctx, settings)
			again.get(t, "/v1/responses/"+old.ID, key.Key, want)
			again.get(t, "/v1/responses/"+fresh, key.Key, 200)
			again.kill(t)
			st, err = store.Open(settings.Database)
			if err != nil {
				t.Fatal(err)
			}
			var current model.ResponseHistory
			var historyCount, usageCount int64
			if st.DB().First(&current, "id = ?", old.ID).Error != nil || !bytes.Equal(current.Ciphertext, old.Ciphertext) || current.Version != old.Version {
				t.Fatal("recovery rewrote or appended the old generation")
			}
			if !known && current.Busy != "uncertain" {
				t.Fatal("unknown history was reopened")
			}
			st.DB().Model(&model.ResponseHistory{}).Count(&historyCount)
			st.DB().Model(&model.UsageInvocation{}).Count(&usageCount)
			if historyCount != 2 || usageCount != 2 || !bytes.Equal(money, wsCrashFinancialImage(t, st)) || f.attempts.Load() != expected+1 || f.httpCalls.Load() != 0 {
				t.Fatal("repeated recovery duplicated history, usage, execution or monetary effects", historyCount, usageCount)
			}
		})
	}
}
