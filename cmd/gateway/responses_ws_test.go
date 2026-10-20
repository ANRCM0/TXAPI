package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	JSON                        json.RawMessage
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
	httpRuntime                          *responsesRuntime
	persistentHistory                    bool
	historyConfig                        config.ResponsesConfig
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
	nativeOverride                       atomic.Pointer[http.HandlerFunc]
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
	if handler := f.nativeOverride.Load(); handler != nil {
		(*handler)(w, r)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		f.httpCalls.Add(1)
		if f.persistentHistory {
			data, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			f.attempts.Add(1)
			f.observations <- wsAssemblyObservation{JSON: data}
			var request struct {
				Model    string
				Stream   bool
				Store    bool
				Previous string `json:"previous_response_id"`
			}
			if json.Unmarshal(data, &request) != nil || request.Model != "m" || request.Store || request.Previous != "" {
				w.WriteHeader(400)
				return
			}
			output := []any{map[string]any{"type": "message", "id": fmt.Sprintf("msg_http_%d", f.httpCalls.Load()), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "HTTP-answer", "annotations": []any{}}}}}
			doc := map[string]any{"id": fmt.Sprintf("resp_http_private_%d", f.httpCalls.Load()), "object": "response", "model": "m", "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}
			w.Header().Set("Content-Type", "application/json")
			if request.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", wsAssemblyJSON(map[string]any{"type": "response.completed", "response": doc}))
			} else {
				_, _ = w.Write(wsAssemblyJSON(doc))
			}
			return
		}
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
		if f.persistentHistory && input == "" {
			if items, ok := request["input"].([]any); ok && len(items) > 0 {
				if item, ok := items[len(items)-1].(map[string]any); ok {
					if value, ok := item["content"].(string); ok {
						input = value
					}
					if parts, ok := item["content"].([]any); ok && len(parts) > 0 {
						if part, ok := parts[len(parts)-1].(map[string]any); ok {
							input, _ = part["text"].(string)
						}
					}
				}
			}
		}
		f.attempts.Add(1)
		round++
		f.observations <- wsAssemblyObservation{Connection: id, Previous: previous, Model: model, Input: input, Key: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), PrivateHeaders: r.Header.Get("Cookie") == "" && r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("Proxy-Authorization") == "", JSON: append([]byte(nil), data...)}
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
		if request["generate"] == false {
			event := wsAssemblyEvent("response.completed", responseID, "completed", 1, []any{})
			var envelope map[string]any
			_ = json.Unmarshal(event, &envelope)
			doc := envelope["response"].(map[string]any)
			doc["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
			if input == "unknown" {
				doc["usage"] = nil
			}
			if connection.Write(ctx, transport.Text, wsAssemblyJSON(envelope)) != nil {
				return
			}
			continue
		}
		output := []any{}
		if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
			call := map[string]any{"type": "function_call", "id": "item_call", "call_id": "call_fixture", "name": "lookup", "arguments": "{}", "status": "completed"}
			pending := map[string]any{"type": "function_call", "id": "item_call", "call_id": "call_fixture", "name": "lookup", "arguments": "", "status": "in_progress"}
			tool, _ := tools[0].(map[string]any)
			if tool["type"] == "namespace" {
				call["namespace"], pending["namespace"] = tool["name"], tool["name"]
				if members, ok := tool["tools"].([]any); ok && len(members) > 0 {
					tool, _ = members[0].(map[string]any)
				}
			}
			events := []any{
				map[string]any{"type": "response.output_item.added", "stream_id": "main", "sequence_number": 1, "item": pending},
				map[string]any{"type": "response.function_call_arguments.delta", "stream_id": "main", "sequence_number": 2, "delta": "{}"},
				map[string]any{"type": "response.function_call_arguments.done", "stream_id": "main", "sequence_number": 3, "arguments": "{}"},
				map[string]any{"type": "response.output_item.done", "stream_id": "main", "sequence_number": 4, "item": call},
			}
			if tool["type"] == "custom" {
				call["type"], pending["type"] = "custom_tool_call", "custom_tool_call"
				call["name"], pending["name"] = tool["name"], tool["name"]
				delete(call, "arguments")
				delete(pending, "arguments")
				call["input"], pending["input"] = "raw 雪🌏\nsecond", ""
				events = []any{
					map[string]any{"type": "response.output_item.added", "sequence_number": 1, "item": pending},
					map[string]any{"type": "response.custom_tool_call_input.delta", "sequence_number": 2, "delta": "raw 雪🌏\n"},
					map[string]any{"type": "response.custom_tool_call_input.delta", "sequence_number": 3, "delta": "second"},
					map[string]any{"type": "response.custom_tool_call_input.done", "sequence_number": 4, "input": call["input"]},
					map[string]any{"type": "response.output_item.done", "sequence_number": 5, "item": call},
				}
			}
			if tool["type"] == "tool_search" {
				call = map[string]any{"type": "tool_search_call", "id": "tsc_fixture", "call_id": "call_fixture", "execution": "client", "status": "completed", "arguments": map[string]any{"query": "雪🌏"}, "future": true}
				pending = map[string]any{"type": "tool_search_call", "id": "tsc_fixture", "call_id": "call_fixture", "execution": "client", "status": "in_progress", "arguments": map[string]any{"query": "雪🌏"}}
				events = []any{map[string]any{"type": "response.output_item.added", "sequence_number": 1, "item": pending}, map[string]any{"type": "response.output_item.done", "sequence_number": 2, "item": call}}
			}
			for _, event := range events {
				if connection.Write(ctx, transport.Text, wsAssemblyJSON(event)) != nil {
					return
				}
				if (input == "block-custom" && event.(map[string]any)["sequence_number"] == 2) || (input == "block-search" && event.(map[string]any)["sequence_number"] == 1) {
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
			}
			output = []any{call}
		} else {
			if connection.Write(ctx, transport.Text, wsAssemblyJSON(map[string]any{"type": "response.output_text.delta", "stream_id": "main", "sequence_number": 1, "delta": "hello"})) != nil {
				return
			}
			if f.persistentHistory {
				output = []any{map[string]any{"id": fmt.Sprintf("msg_%d_%d", id, round), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "hello", "annotations": []any{}}}}}
				if input == "reasoning" {
					output = append([]any{map[string]any{"id": fmt.Sprintf("rs_%d_%d", id, round), "type": "reasoning", "encrypted_content": "fixture-history-opaque", "summary": []any{map[string]any{"type": "summary_text", "text": "summary"}}, "future": true}}, output...)
				}
			}
		}
		status := "completed"
		if f.persistentHistory && (input == "failed" || input == "incomplete" || input == "cancelled") {
			status = input
		}
		if connection.Write(ctx, transport.Text, wsAssemblyEvent("response."+status, responseID, status, 6, output)) != nil {
			return
		}
	}
}
func newWSAssemblyFixture(t *testing.T) *wsAssemblyFixture {
	return newWSAssemblyFixtureWithUsage(t, false)
}

func newWSAssemblyFixtureWithUsage(t *testing.T, recordUsage bool) *wsAssemblyFixture {
	return newWSAssemblyFixtureWithHistory(t, recordUsage, false)
}

func newWSAssemblyFixtureWithHistory(t *testing.T, recordUsage, persistentHistory bool) *wsAssemblyFixture {
	return newWSAssemblyFixtureWithRecordLimit(t, recordUsage, persistentHistory, 8)
}

func newWSAssemblyFixtureWithRecordLimit(t *testing.T, recordUsage, persistentHistory bool, records int) *wsAssemblyFixture {
	t.Helper()
	dir := t.TempDir()
	credentialKeys := runtimeKeyFile(t, "native-credentials", 21, 25)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	f := &wsAssemblyFixture{t: t, ctx: ctx, cancel: cancel, observations: make(chan wsAssemblyObservation, 16), blocked: make(chan struct{}, 4), release: make(chan struct{}), persistentHistory: persistentHistory}
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
	if persistentHistory {
		localHTTP := http.DefaultTransport.(*http.Transport).Clone()
		t.Cleanup(localHTTP.CloseIdleConnections)
		gatewayOptions.Client = &http.Client{Transport: generationTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" || r.URL.Path != "/v1/responses" {
				return nil, errors.New("fixture forbids external inference")
			}
			u, err := url.Parse(f.upstream.URL)
			if err != nil {
				return nil, err
			}
			copy := r.Clone(r.Context())
			endpoint := *r.URL
			endpoint.Scheme, endpoint.Host = u.Scheme, u.Host
			copy.URL, copy.Host = &endpoint, u.Host
			return localHTTP.RoundTrip(copy)
		})}
	}
	if recordUsage {
		gatewayOptions.UsageHistory = f.st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: records, OwnerRecords: records, RetentionSeconds: 3600})
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
	if persistentHistory {
		f.historyConfig = responsesRuntimeConfig(runtimeKeyFile(t, "stored-history", 51, 52))
		if int64(records) > f.historyConfig.OwnerRecords {
			f.historyConfig.OwnerRecords = int64(records)
		}
		if f.historyConfig.InstanceRecords <= f.historyConfig.OwnerRecords {
			f.historyConfig.InstanceRecords = 2 * f.historyConfig.OwnerRecords
		}
	}
	f.startPublicRuntimes(t, limiter, recordUsage)
	return f
}

func (f *wsAssemblyFixture) startPublicRuntimes(t *testing.T, limiter access.Limiter, recordUsage bool) {
	t.Helper()
	var err error
	if f.persistentHistory {
		f.httpRuntime, err = startResponsesRuntime(f.historyConfig, f.st, f.gw)
		if err != nil {
			t.Fatal(err)
		}
	}
	f.runtime, err = startResponsesWSRuntimeWithFactory(config.ResponsesWSConfig{Enabled: true, PersistentHistory: f.persistentHistory, Models: []string{"m"}}, f.st, f.gw, limiter, func(root context.Context, limits contract.Limits) (gateway.NativeWSResource, error) {
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
	}, f.httpRuntime.History())
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
	engine := server.NewEngine(server.Deps{Generation: gateway.NewHTTPExecutor(f.gw), TokenAuth: identity.NewTokenAuthenticator(f.st), Control: controlplane.New(persistence.Control(f.st)), Configuration: f.configuration, Metrics: f.gw.Metrics(), UsageHistory: usageconsole.New(f.st.UsageHistory(recordUsage, 3600), controlplane.New(persistence.Control(f.st)), limiter), Responses: f.httpRuntime.Handler(), ResponsesWS: f.runtime.Handler(), AdminAuth: middleware.AdminAuth("assembly-admin-fixture"), RateLimiter: limiter, RateGate: access.NewRateGate(limiter)})
	f.front = httptest.NewServer(engine)
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
	if f.httpRuntime != nil {
		f.httpRuntime.Close()
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
