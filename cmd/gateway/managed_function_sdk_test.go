package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	cfgv1 "minigateway/internal/foundation/contracts/configuration/v1"
	cfgv2 "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/foundation/upstreamhttp"
	"minigateway/internal/gateway"
	"minigateway/internal/management"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
	"minigateway/internal/server"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

const managedFunctionSDKModel = "synthetic-model"
const managedFunctionSDKCallID = "call_managed_weather"
const managedFunctionSDKArguments = "{\"city\":\"\u4e0a\u6d77\"}"
const managedFunctionSDKAnswer = "\u4e0a\u6d77: sunny, 22 C."
const managedFunctionSDKAccess = "managed-function-access-fixture-only"

type managedFunctionSDKObservation struct {
	body  map[string]json.RawMessage
	input []map[string]json.RawMessage
}

type managedFunctionSDKFixture struct {
	ctx          context.Context
	st           *store.Store
	account      auth.Account
	key          *model.Token
	ownerID      uint
	bound        *cfgv2.ManagedChannel
	front        *httptest.Server
	upstream     *httptest.Server
	gw           *gateway.Gateway
	attempts     atomic.Int64
	catalogs     atomic.Int64
	observations chan managedFunctionSDKObservation
}

func newManagedFunctionSDKFixture(t *testing.T) *managedFunctionSDKFixture {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	f := &managedFunctionSDKFixture{ctx: ctx, observations: make(chan managedFunctionSDKObservation, 32)}
	var err error
	f.st, err = store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "managed-function.db"),
		CredentialKeysFile: runtimeKeyFile(t, "managed-function-credentials", 81, 83)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	owner, err := f.st.BootstrapAdmin("managed-function-admin", "managed-function-password-fixture-only")
	if err != nil {
		t.Fatal(err)
	}
	f.ownerID = owner.ID
	f.key, err = f.st.IssueToken(owner.ID, &store.TokenInput{Name: "managed-function-client", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.account, err = f.st.UpstreamAccounts().SaveAuthorization(ctx, auth.AuthorizationWrite{
		ID: "ua_00000000000000000000000000000083", Provider: "openai", Name: "synthetic function SIWC", ClientID: "synthetic-issued-client",
		Identity: auth.Identity{Issuer: "https://auth.openai.com", Subject: "synthetic-function-subject"},
		Tokens: auth.Tokens{AccessToken: auth.NewSecret(managedFunctionSDKAccess), RefreshToken: auth.NewSecret("managed-function-refresh-fixture-only"),
			IDToken: auth.NewSecret("managed-function-id-fixture-only"), Nonce: auth.NewSecret("managed-function-nonce-fixture-only"),
			ExpiresAt: time.Now().Add(time.Hour), ScopesKnown: true, Scopes: []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.bound, err = f.st.ManagedChannels().CreateManagedChannel(ctx, &cfgv2.ManagedChannelInput{
		Channel: cfgv1.ChannelInput{Name: "managed-function", Type: "openai_siwc", BaseURL: "https://api.openai.com/v1", Models: []string{managedFunctionSDKModel}},
		Binding: cfgv2.ManagedBindingInput{AccountID: f.account.ID, OwnerID: owner.ID, TokenID: f.key.ID, Model: managedFunctionSDKModel},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.openai.com" || r.Header.Get("Authorization") != "Bearer "+managedFunctionSDKAccess ||
			r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("managed function fixture received wrong target, credential or private header")
			http.Error(w, "fixture credential isolation failed", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			f.catalogs.Add(1)
			if r.Method != http.MethodGet {
				t.Error("managed function directory was not GET")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[{"slug":"synthetic-model","visibility":"list"}]}`)
		case "/v1/responses":
			f.attempts.Add(1)
			var body map[string]json.RawMessage
			if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil ||
				string(body["store"]) != "false" || string(body["stream"]) != "true" || string(body["model"]) != `"synthetic-model"` ||
				len(body["previous_response_id"]) != 0 || len(body["conversation"]) != 0 {
				t.Error("managed function fixture received wrong request envelope")
				http.Error(w, "fixture envelope failed", http.StatusBadRequest)
				return
			}
			var input []map[string]json.RawMessage
			if json.Unmarshal(body["input"], &input) != nil || (len(input) != 1 && len(input) != 3) {
				t.Error("managed function fixture received unexpected input count")
				http.Error(w, "fixture input failed", http.StatusBadRequest)
				return
			}
			f.observations <- managedFunctionSDKObservation{body: body, input: input}
			w.Header().Set("Content-Type", "text/event-stream")
			sequence := 0
			event := func(kind string, fields map[string]any) {
				fields["type"], fields["sequence_number"] = kind, sequence
				sequence++
				data, e := json.Marshal(fields)
				if e != nil {
					t.Error("managed function fixture could not encode event")
					return
				}
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
				w.(http.Flusher).Flush()
			}
			first := len(input) == 1
			identifier, inputTokens, outputTokens := "remote_managed_weather_final", 11, 13
			if first {
				identifier, inputTokens, outputTokens = "remote_managed_weather_call", 5, 7
			}
			response := func(output []any, status string, usage any) map[string]any {
				return map[string]any{"id": identifier, "object": "response", "created_at": 1, "model": managedFunctionSDKModel,
					"status": status, "output": output, "usage": usage, "error": nil, "incomplete_details": nil}
			}
			event("response.created", map[string]any{"response": response([]any{}, "in_progress", nil)})
			var item map[string]any
			if first {
				item = map[string]any{"type": "function_call", "id": "fc_managed_weather", "call_id": managedFunctionSDKCallID,
					"name": "weather", "arguments": managedFunctionSDKArguments, "status": "completed"}
				pending := map[string]any{"type": "function_call", "id": "fc_managed_weather", "call_id": managedFunctionSDKCallID,
					"name": "weather", "arguments": "", "status": "in_progress"}
				event("response.output_item.added", map[string]any{"output_index": 0, "item": pending})
				for _, delta := range []string{`{"city":"`, "\u4e0a", "\u6d77", `"}`} {
					event("response.function_call_arguments.delta", map[string]any{"item_id": "fc_managed_weather", "output_index": 0, "delta": delta})
				}
				event("response.function_call_arguments.done", map[string]any{"item_id": "fc_managed_weather", "output_index": 0, "arguments": managedFunctionSDKArguments})
			} else {
				part := map[string]any{"type": "output_text", "text": managedFunctionSDKAnswer, "annotations": []any{}}
				item = map[string]any{"type": "message", "id": "msg_managed_weather", "role": "assistant", "status": "completed", "content": []any{part}}
				event("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_managed_weather", "role": "assistant", "status": "in_progress", "content": []any{}}})
				event("response.content_part.added", map[string]any{"item_id": "msg_managed_weather", "output_index": 0, "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				event("response.output_text.delta", map[string]any{"item_id": "msg_managed_weather", "output_index": 0, "content_index": 0, "delta": managedFunctionSDKAnswer})
				event("response.output_text.done", map[string]any{"item_id": "msg_managed_weather", "output_index": 0, "content_index": 0, "text": managedFunctionSDKAnswer})
				event("response.content_part.done", map[string]any{"item_id": "msg_managed_weather", "output_index": 0, "content_index": 0, "part": part})
			}
			event("response.output_item.done", map[string]any{"output_index": 0, "item": item})
			event("response.completed", map[string]any{"response": response([]any{item}, "completed", map[string]int{
				"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": inputTokens + outputTokens})})
		default:
			t.Errorf("managed function fixture unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.upstream.Close)
	endpoint, err := url.Parse(f.upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := smokeTransport(endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	caller := upstreamhttp.NewCaller(client)
	t.Cleanup(caller.Close)
	limiter := access.NewMemoryLimiter()
	t.Cleanup(limiter.(interface{ Close() }).Close)
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	f.gw, err = gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: f.st.Channels(),
		BillingMode: billing.ModeDisabled, RetryTimes: 3, Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: capacity}, Client: client,
		UsageHistory: f.st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 32, OwnerRecords: 16, RetentionSeconds: 3600}),
		ManagedInference: &gateway.ManagedInferenceOptions{Enabled: true, Credentials: f.st.InferenceCredentials(), DirectoryCaller: caller,
			ValidateModel: openaiauth.ValidateVisibleModel, FailureRecorder: f.st.InferenceEligibility(), ClassifyFailure: openaiauth.ClassifyInferenceFailure}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.gw.Close)
	runtime, err := startResponsesRuntime(responsesRuntimeConfig(runtimeKeyFile(t, "managed-function-content", 87, 89)), f.st, f.gw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	configuration := management.NewConfiguration(persistence.Configuration(f.st), func() error {
		channels, err := f.st.ChannelSnapshot()
		if err != nil {
			return err
		}
		return f.gw.Publish(channels)
	}, nil)
	configuration.SetManagedRepository(f.st.ManagedChannels())
	configuration.SetEligibilityRepository(f.st.InferenceEligibility())
	engine := server.NewEngine(server.Deps{Generation: gateway.NewHTTPExecutor(f.gw), TokenAuth: identity.NewTokenAuthenticator(f.st),
		Control: controlplane.New(persistence.Control(f.st)), Configuration: configuration, ManagedConfiguration: configuration, ManagedEligibility: configuration,
		Metrics: f.gw.Metrics(), Responses: runtime.Handler(), AdminAuth: middleware.AdminAuth("managed-function-admin-fixture-only"),
		RateLimiter: limiter, RateGate: access.NewRateGate(limiter)})
	f.front = httptest.NewServer(engine)
	t.Cleanup(f.front.Close)
	return f
}

// Only the independent pinned-SDK harness starts this helper. The factory also
// serves ordinary tests of current account/key authority between tool rounds.
func TestManagedFunctionPythonSDKProcess(t *testing.T) {
	directory := os.Getenv("TXAPI_TEST_MANAGED_FUNCTION_SDK_DIRECTORY")
	if directory == "" {
		t.Skip("only the bounded Python SDK harness starts this helper")
	}
	f := newManagedFunctionSDKFixture(t)
	data, err := json.Marshal(map[string]string{"base": f.front.URL, "key": f.key.Key})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ready.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "done")); err == nil {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("managed function SDK did not finish before fixture deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if f.attempts.Load() != 2 || f.catalogs.Load() != 1 {
		t.Fatalf("managed SDK must send two POSTs and one directory GET, got %d/%d", f.attempts.Load(), f.catalogs.Load())
	}
	var observations []managedFunctionSDKObservation
	for len(observations) < 2 {
		select {
		case row := <-f.observations:
			observations = append(observations, row)
		default:
			t.Fatal("missing managed function SDK wire observation")
		}
	}
	first, second := observations[0], observations[1]
	if len(first.input) != 1 || len(second.input) != 3 || string(first.body["tools"]) != string(second.body["tools"]) ||
		string(first.body["tool_choice"]) != `"auto"` || string(first.body["tool_choice"]) != string(second.body["tool_choice"]) ||
		string(first.input[0]["content"]) != string(second.input[0]["content"]) {
		t.Fatal("SDK changed original message, function declarations or tool choice")
	}
	var arguments, callID, resultID, resultText string
	call, result := second.input[1], second.input[2]
	if string(call["type"]) != `"function_call"` || string(call["name"]) != `"weather"` || string(call["status"]) != `"completed"` ||
		string(result["type"]) != `"function_call_output"` || json.Unmarshal(call["arguments"], &arguments) != nil ||
		json.Unmarshal(call["call_id"], &callID) != nil || json.Unmarshal(result["call_id"], &resultID) != nil || json.Unmarshal(result["output"], &resultText) != nil ||
		arguments != managedFunctionSDKArguments || callID != managedFunctionSDKCallID || resultID != callID {
		t.Fatal("SDK lost original function identity, arguments or association")
	}
	var weather struct {
		City         string `json:"city"`
		Forecast     string `json:"forecast"`
		TemperatureC int    `json:"temperature_c"`
	}
	if json.Unmarshal([]byte(resultText), &weather) != nil || weather.City != "\u4e0a\u6d77" || weather.Forecast != "sunny" || weather.TemperatureC != 22 {
		t.Fatal("client did not return its fixture weather result")
	}
	var usage []model.UsageInvocation
	if err := f.st.DB().Find(&usage).Error; err != nil || len(usage) != 2 {
		t.Fatal("managed SDK usage count", len(usage), err)
	}
	for _, row := range usage {
		if row.Transport != "http" || row.State != "sealed" || row.AttemptCount != 1 || !row.Host.ProtocolComplete ||
			!row.Host.DeliveryCommitted || row.Host.HTTPStatus != http.StatusOK || row.Host.BytesWritten <= 0 || row.UserID != f.ownerID || row.TokenID != f.key.ID {
			t.Fatal("managed SDK did not seal its own single HTTP invocation")
		}
	}
	var receipts []model.UsageAttemptReceipt
	if err := f.st.DB().Order("input_tokens").Find(&receipts).Error; err != nil || len(receipts) != 2 {
		t.Fatal("managed SDK receipt count", len(receipts), err)
	}
	for index, row := range receipts {
		input, output := int64(5+6*index), int64(7+6*index)
		if !row.MayDispatch || row.CredentialID != f.bound.Credential.CredentialID || !row.Host.ProtocolComplete ||
			!row.Host.DeliveryCommitted || row.Host.HTTPStatus != http.StatusOK || row.Host.BytesWritten <= 0 ||
			row.InputTokens == nil || *row.InputTokens != input || row.OutputTokens == nil || *row.OutputTokens != output || row.TotalTokens == nil || *row.TotalTokens != input+output {
			t.Fatal("managed SDK lost original per-attempt usage or managed credential")
		}
	}
	for _, table := range []string{"billing_reservations", "billing_attempts", "response_histories"} {
		var count int64
		if err := f.st.DB().Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("managed store=false SDK produced billing/history state", table, count, err)
		}
	}
	for _, table := range []string{"users", "tokens"} {
		var count int64
		if err := f.st.DB().Table(table).Where("used_quota != 0").Count(&count).Error; err != nil || count != 0 {
			t.Fatal("managed SDK changed quota", table, count, err)
		}
	}
}
