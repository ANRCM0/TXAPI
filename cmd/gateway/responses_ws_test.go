package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	bridge "minigateway/internal/compat/websocket"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	identity "minigateway/internal/foundation/identity"
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

type wsAssemblyObservation struct {
	Connection                  int64
	Previous, Model, Input, Key string
	PrivateHeaders              bool
}
type wsAssemblyResource struct {
	manager     *transport.Manager
	endpoint    string
	prefix      int
	serial      atomic.Int64
	closed      chan struct{}
	once        sync.Once
	beforeClose func(context.Context) error
}

func (r *wsAssemblyResource) Dial(ctx context.Context, key string) (execution.Connection, error) {
	connection, err := r.manager.Dial(ctx, r.endpoint, http.Header{"Authorization": {"Bearer " + key}})
	if err != nil {
		return nil, err
	}
	return bridge.Adapt(execution.ConnectionID(fmt.Sprintf("up-%d-%d", r.prefix, r.serial.Add(1))), connection), nil
}
func (r *wsAssemblyResource) Close(ctx context.Context) error {
	if r.beforeClose != nil {
		if err := r.beforeClose(ctx); err != nil {
			return err
		}
	}
	if err := r.manager.Close(ctx); err != nil {
		return err
	}
	r.once.Do(func() { close(r.closed) })
	return nil
}

type wsAssemblyFixture struct {
	t                                    *testing.T
	ctx                                  context.Context
	cancel                               context.CancelFunc
	st                                   *store.Store
	gw                                   *gateway.Gateway
	capacity                             *access.Capacity
	runtime                              *responsesWSRuntime
	configuration                        *management.Configuration
	front, upstream                      *httptest.Server
	upstreamManager                      *transport.Manager
	upstreamWorkers                      sync.WaitGroup
	key                                  string
	user, keyID, channelID, credentialID uint
	resources                            []*wsAssemblyResource
	failPrepare                          atomic.Bool
	attempts, httpCalls, connections     atomic.Int64
	observations                         chan wsAssemblyObservation
	blocked                              chan struct{}
	release                              chan struct{}
}

func wsAssemblyJSON(value any) []byte { body, _ := json.Marshal(value); return body }
func wsAssemblyEvent(typ, id, status string, seq int, output any) []byte {
	response := map[string]any{"id": id, "object": "response", "model": "m", "status": status, "output": output, "usage": nil}
	if status == "completed" {
		response["usage"] = map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}
	}
	return wsAssemblyJSON(map[string]any{"type": typ, "stream_id": "main", "sequence_number": seq, "response": response})
}
func (f *wsAssemblyFixture) upstreamHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		f.httpCalls.Add(1)
		w.WriteHeader(500)
		return
	}
	connection, err := f.upstreamManager.Accept(w, r)
	if err != nil {
		return
	}
	f.upstreamWorkers.Add(1)
	defer f.upstreamWorkers.Done()
	ctx, cancel := context.WithCancel(f.ctx)
	readDone := make(chan struct{})
	requests := make(chan []byte, 1)
	go func() {
		defer close(readDone)
		for {
			typ, data, err := connection.Read(ctx)
			if err != nil || typ != transport.Text {
				cancel()
				return
			}
			select {
			case requests <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); connection.Close(); <-readDone }()
	id := f.connections.Add(1)
	round := 0
	for {
		var data []byte
		select {
		case data = <-requests:
		case <-ctx.Done():
			return
		}
		var request map[string]any
		if json.Unmarshal(data, &request) != nil {
			return
		}
		previous, _ := request["previous_response_id"].(string)
		model, _ := request["model"].(string)
		input, _ := request["input"].(string)
		f.attempts.Add(1)
		round++
		f.observations <- wsAssemblyObservation{Connection: id, Previous: previous, Model: model, Input: input, Key: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), PrivateHeaders: r.Header.Get("Cookie") == "" && r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("Proxy-Authorization") == ""}
		if request["type"] != "response.create" || request["store"] != false || request["stream_id"] != "main" || model != "m" {
			return
		}
		responseID := fmt.Sprintf("resp_%d_%d", id, round)
		if err := connection.Write(ctx, transport.Text, wsAssemblyEvent("response.created", responseID, "in_progress", 0, []any{})); err != nil {
			return
		}
		if input == "block" {
			select {
			case f.blocked <- struct{}{}:
			default:
			}
			select {
			case <-f.release:
			case <-ctx.Done():
				return
			}
		}
		output := []any{}
		if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
			call := map[string]any{"type": "function_call", "id": "item_call", "call_id": "call_fixture", "name": "lookup", "arguments": "{}", "status": "completed"}
			events := []any{
				map[string]any{"type": "response.output_item.added", "stream_id": "main", "sequence_number": 1, "item": map[string]any{"type": "function_call", "id": "item_call", "call_id": "call_fixture", "name": "lookup", "arguments": "", "status": "in_progress"}},
				map[string]any{"type": "response.function_call_arguments.delta", "stream_id": "main", "sequence_number": 2, "delta": "{}"},
				map[string]any{"type": "response.function_call_arguments.done", "stream_id": "main", "sequence_number": 3, "arguments": "{}"},
				map[string]any{"type": "response.output_item.done", "stream_id": "main", "sequence_number": 4, "item": call},
			}
			for _, event := range events {
				if connection.Write(ctx, transport.Text, wsAssemblyJSON(event)) != nil {
					return
				}
			}
			output = []any{call}
		} else {
			if connection.Write(ctx, transport.Text, wsAssemblyJSON(map[string]any{"type": "response.output_text.delta", "stream_id": "main", "sequence_number": 1, "delta": "hello"})) != nil {
				return
			}
		}
		if connection.Write(ctx, transport.Text, wsAssemblyEvent("response.completed", responseID, "completed", 5, output)) != nil {
			return
		}
	}
}
func newWSAssemblyFixture(t *testing.T) *wsAssemblyFixture {
	return newWSAssemblyFixtureWithUsage(t, false)
}

func newWSAssemblyFixtureWithUsage(t *testing.T, recordUsage bool) *wsAssemblyFixture {
	t.Helper()
	dir := t.TempDir()
	credentialKeys := runtimeKeyFile(t, "native-credentials", 21, 25)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	f := &wsAssemblyFixture{t: t, ctx: ctx, cancel: cancel, observations: make(chan wsAssemblyObservation, 16), blocked: make(chan struct{}, 4), release: make(chan struct{})}
	t.Cleanup(f.close)
	var err error
	f.st, err = store.Open(config.DatabaseConfig{Path: filepath.Join(dir, "native-ws.db"), CredentialKeysFile: credentialKeys})
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.st.CreateUser(&store.UserInput{Username: "native-runtime", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All, RPMLimit: 100, TPMLimit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	f.key, f.user, f.keyID = key.Key, user.ID, key.ID
	ch, err := f.st.CreateChannel(&store.ChannelInput{Name: "native-official", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"alias"}, ModelMapping: map[string]string{"alias": "m"}, APIKey: "upstream-main-fixture", Weight: 1, Status: "enabled"})
	if err != nil {
		t.Fatal(err)
	}
	f.channelID = ch.ID
	credentials := f.st.ListCredentials(ch.ID)
	if len(credentials) != 1 {
		t.Fatal("fixture credential missing")
	}
	f.credentialID = credentials[0].ID
	f.capacity, err = access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 4})
	if err != nil {
		t.Fatal(err)
	}
	limiter := access.NewMemoryLimiter()
	gatewayOptions := gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: f.st.Channels(), BillingMode: billing.ModeDisabled, Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: f.capacity}}
	if recordUsage {
		gatewayOptions.UsageHistory = f.st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 8, OwnerRecords: 8, RetentionSeconds: 3600})
	}
	f.gw, err = gateway.New(gatewayOptions)
	if err != nil {
		t.Fatal(err)
	}
	f.upstreamManager, err = transport.New(ctx, transport.Options{Connections: 16, MaxMessageBytes: 256 << 10, HandshakeTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, MaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.upstream = httptest.NewServer(http.HandlerFunc(f.upstreamHandler))
	f.runtime, err = startResponsesWSRuntimeWithFactory(config.ResponsesWSConfig{Enabled: true, Models: []string{"m"}}, f.st, f.gw, limiter, func(root context.Context, limits contract.Limits) (gateway.NativeWSResource, error) {
		if f.failPrepare.Load() {
			return nil, errors.New("fixture prepare failure")
		}
		manager, err := transport.New(root, transport.Options{Connections: 64, MaxMessageBytes: limits.MaxMessageBytes, HandshakeTimeout: time.Second, ReadTimeout: limits.RoundTimeout, WriteTimeout: time.Second, MaxLifetime: limits.MaxLifetime})
		if err != nil {
			return nil, err
		}
		resource := &wsAssemblyResource{manager: manager, endpoint: "ws" + strings.TrimPrefix(f.upstream.URL, "http"), prefix: len(f.resources) + 1, closed: make(chan struct{})}
		f.resources = append(f.resources, resource)
		return resource, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.configuration = management.NewConfiguration(persistence.Configuration(f.st), func() error {
		channels, err := f.st.ChannelSnapshot()
		if err != nil {
			return err
		}
		return f.runtime.Publish(channels)
	}, nil)
	engine := server.NewEngine(server.Deps{Generation: gateway.NewHTTPExecutor(f.gw), TokenAuth: identity.NewTokenAuthenticator(f.st), Control: controlplane.New(persistence.Control(f.st)), Configuration: f.configuration, Metrics: f.gw.Metrics(), UsageHistory: usageconsole.New(f.st.UsageHistory(recordUsage, 3600), controlplane.New(persistence.Control(f.st)), limiter), ResponsesWS: f.runtime.Handler(), AdminAuth: middleware.AdminAuth("assembly-admin-fixture"), RateLimiter: limiter, RateGate: access.NewRateGate(limiter)})
	f.front = httptest.NewServer(engine)
	return f
}
func (f *wsAssemblyFixture) close() {
	if f.runtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := f.runtime.Close(ctx)
		cancel()
		if err != nil {
			f.t.Error("native runtime did not drain", err)
			return
		}
	}
	if f.front != nil {
		f.front.Close()
	}
	f.cancel()
	if f.upstreamManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = f.upstreamManager.Close(ctx)
		cancel()
	}
	if f.upstream != nil {
		f.upstream.Close()
	}
	f.upstreamWorkers.Wait()
	if f.gw != nil {
		f.gw.Close()
	}
	if f.capacity != nil {
		f.capacity.Close()
	}
	if f.st != nil {
		_ = f.st.Close()
	}
}
func (f *wsAssemblyFixture) dial(t *testing.T) *ws.Conn {
	t.Helper()
	connection, _, err := ws.Dial(f.ctx, "ws"+strings.TrimPrefix(f.front.URL, "http")+"/v1/responses", &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + f.key}, "Cookie": {"client-cookie=do-not-forward"}, "X-Forwarded-For": {"192.0.2.1"}, "Proxy-Authorization": {"do-not-forward"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection
}
func (f *wsAssemblyFixture) send(t *testing.T, c *ws.Conn, previous, input string, tools bool) {
	t.Helper()
	request := map[string]any{"type": "response.create", "model": "alias", "input": input}
	if previous != "" {
		request["previous_response_id"] = previous
	}
	if tools {
		request["tools"] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
	}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
}
func (f *wsAssemblyFixture) receiveRound(t *testing.T, c *ws.Conn) (string, []string) {
	t.Helper()
	var kinds []string
	for {
		_, body, err := c.Read(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if json.Unmarshal(body, &event) != nil {
			t.Fatal("invalid WS JSON")
		}
		typ, _ := event["type"].(string)
		kinds = append(kinds, typ)
		if typ == "error" {
			t.Fatalf("unexpected problem: %s", body)
		}
		if typ == "response.completed" {
			response := event["response"].(map[string]any)
			f.waitReleased(t)
			return response["id"].(string), kinds
		}
	}
}
func (f *wsAssemblyFixture) waitReleased(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot := f.capacity.Snapshot()
		if snapshot.Requests == 0 && snapshot.Attempts == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("request/attempt admission did not join")
		}
		time.Sleep(time.Millisecond)
	}
}
