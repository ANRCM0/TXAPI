package runtime_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/foundation/runtime"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
)

func TestResultBridgePreservesUnknownExecutionAndOwnsMutableValues(t *testing.T) {
	in := gateway.RequestResult{Operation: execution.Responses, ResponseStatus: "in_progress", UpstreamResponseID: "native-id", BillingID: "ledger-id", PriceVersion: "pinned-price", BillingState: "pending",
		Identity: gateway.Identity{UserID: 2, TokenID: 3, Policy: &access.Policy{Mode: access.Allowlist, Models: []string{"m"}}, AllowedModels: []string{"m"}},
		Result:   core.Result{Phase: core.ResponseReceived, StatusCode: 502, HTTPStatus: 200, Committed: true, Termination: core.ClientWriteError, Usage: &core.Usage{PromptTokens: 4}, Error: &core.Error{StatusCode: 502, Error: core.ErrorDetail{Type: "interrupted", Code: "known-code"}}, BytesWritten: 17, Duration: time.Second},
		Attempts: []gateway.AttemptResult{{Operation: execution.Responses, ChannelID: 4, Result: core.Result{Phase: core.Dispatching}}}}
	out := runtime.FromGateway(in)
	if out.Phase != execution.ResponseReceived || !out.Committed || out.HTTPStatus != 200 || out.ProtocolComplete || out.Termination != execution.ClientWriteError || out.Native.ID != "native-id" || out.Native.Status != "in_progress" || out.BillingID != "ledger-id" || out.PriceVersion != "pinned-price" || out.Attempts[0].Phase != execution.Dispatching || string(out.Error.Error.Code) != `"known-code"` || out.BytesWritten != 17 || out.Duration != time.Second {
		t.Fatalf("execution facts lost: %+v", out)
	}
	out.Usage.PromptTokens = 999
	out.Identity.Policy.Models[0] = "changed"
	out.Identity.AllowedModels[0] = "changed"
	out.Error.Error.Message = "changed"
	out.Attempts[0].ChannelID = 99
	if in.Usage.PromptTokens != 4 || in.Identity.Policy.Models[0] != "m" || in.Identity.AllowedModels[0] != "m" || in.Error.Error.Message != "" || in.Attempts[0].ChannelID != 4 {
		t.Fatal("bridge leaked mutable source values")
	}
}

type shortOutput struct{ state execution.OutputState }

func (o *shortOutput) Commit(context.Context, execution.ResponseMetadata) error {
	o.state = execution.OutputState{Committed: true, HTTPStatus: 200}
	return nil
}
func (o *shortOutput) Write(context.Context, []byte) (int, error) { return 1, nil }
func (o *shortOutput) Flush(context.Context) error                { return nil }
func (o *shortOutput) State() execution.OutputState               { return o.state }

func TestPublicExecutorUsesOriginalCoordinatorAndPreservesShortWrite(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-id","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	g, err := gateway.New(gateway.Options{BillingMode: billing.ModeDisabled, Channels: []model.Channel{{ID: 1, Name: "fixture", Type: "openai", BaseURL: upstream.URL, Status: model.StatusEnabled, Weight: 1, Models: model.StringList{"m"}, Credentials: []model.Credential{{ID: 2, APIKey: "fixture", Status: model.CredentialActive, Weight: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	executor := runtime.NewExecutor(g)
	in := execution.Invocation{Operation: execution.Chat, Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)}
	unauthorized := executor.Execute(context.Background(), httpoutput.New(httptest.NewRecorder()), in)
	if unauthorized.StatusCode != 401 || calls != 0 {
		t.Fatalf("missing identity executed: %+v calls=%d", unauthorized, calls)
	}
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 3, TokenID: 4, Policy: access.Policy{Mode: access.All}})
	result := executor.Execute(ctx, &shortOutput{}, in)
	if calls != 1 || !result.Committed || result.HTTPStatus != 200 || result.Termination != execution.ClientWriteError || result.Error == nil {
		t.Fatalf("short write lost: %+v calls=%d", result, calls)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	result = executor.Execute(canceled, httpoutput.New(httptest.NewRecorder()), in)
	if calls != 1 || result.Phase != execution.NotDispatched || result.ProtocolComplete {
		t.Fatalf("canceled request dispatched: %+v calls=%d", result, calls)
	}
	if !errors.Is(canceled.Err(), context.Canceled) {
		t.Fatal("fixture cancellation failed")
	}
}
