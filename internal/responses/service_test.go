package responses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"minigateway/internal/compat/httpoutput"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	app "minigateway/internal/responses"
	"minigateway/internal/store"
)

type responseExecutor struct {
	calls   atomic.Int32
	status  string
	unknown bool
	before  func()
}

func (e *responseExecutor) Execute(ctx context.Context, output execv1.Output, in execv1.Invocation) execv1.Outcome {
	e.calls.Add(1)
	if e.before != nil {
		e.before()
	}
	status := e.status
	if status == "" {
		status = "completed"
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(in.Body, &request)
	content := `{"id":"` + in.OutputID + `","object":"response","model":"m","status":"` + status + `","output":[{"type":"message","id":"msg-native","role":"assistant","content":[{"type":"output_text","text":"retained native answer"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	contentType := "application/json"
	if request.Stream {
		contentType = "text/event-stream"
		content = "event: response." + status + "\ndata: {\"type\":\"response." + status + "\",\"sequence_number\":0,\"response\":" + content + "}\n\n"
	}
	if e.unknown {
		content = `{"partial":"unknown execution"}`
	}
	result := execv1.Outcome{Operation: protocol.Responses, Native: execv1.NativeCompletion{Status: status, ID: "resp-private-upstream"},
		Result: execv1.Result{StatusCode: 200, Phase: execv1.ResponseReceived, Termination: execv1.Success, ProtocolComplete: !e.unknown,
			Usage: &execv1.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}}
	if err := output.Commit(ctx, execv1.ResponseMetadata{StatusCode: 200, ContentType: contentType, Values: map[string]string{"X-Request-ID": "first-delivery-trace"}}); err != nil {
		result.Error = &execv1.Error{StatusCode: 502, Error: execv1.ErrorDetail{Type: "fixture_output_error", Message: err.Error()}}
		result.StatusCode, result.ProtocolComplete = 502, false
		return result
	}
	if _, err := output.Write(ctx, []byte(content)); err != nil {
		result.Error = &execv1.Error{StatusCode: 502, Error: execv1.ErrorDetail{Type: "fixture_output_error", Message: err.Error()}}
		result.StatusCode, result.ProtocolComplete = 502, false
	}
	if request.Stream {
		_ = output.Flush(ctx)
	}
	result.Committed, result.HTTPStatus = output.State().Committed, output.State().HTTPStatus
	if e.unknown || status == "failed" || status == "cancelled" {
		result.StatusCode, result.Termination = 502, execv1.UpstreamError
		result.Error = &execv1.Error{StatusCode: 502, Error: execv1.ErrorDetail{Type: "upstream_error", Message: "Upstream generation failed."}}
	}
	return result
}

type responseFixture struct {
	st        *store.Store
	path      string
	binding   access.Binding
	principal access.Principal
	executor  *responseExecutor
	service   *app.Service
}

func newResponseFixture(t *testing.T) *responseFixture {
	t.Helper()
	f := &responseFixture{path: filepath.Join(t.TempDir(), "responses.db"), executor: &responseExecutor{}}
	var err error
	f.st, err = store.Open(config.DatabaseConfig{Driver: "sqlite", Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	owner, err := f.st.CreateUser(&store.UserInput{Username: "response-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.st.IssueToken(owner.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.binding = access.Binding{UserID: owner.ID, TokenID: key.ID}
	f.principal, err = f.st.ResolvePrincipal(context.Background(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	f.service = f.newService(t, f.st)
	return f
}

func (f *responseFixture) newService(t *testing.T, receipts persistence.ReceiptBackend) *app.Service {
	t.Helper()
	archive, err := generation.NewArchive(generation.ArchiveOptions{Store: persistence.Archive(f.st), Resolve: f.st.ResolvePrincipal,
		ActiveKey: "fixture-key", Keys: map[string][]byte{"fixture-key": bytes.Repeat([]byte{23}, 32)}, Retention: time.Hour,
		MaxRecordBytes: 65536, Limits: model.GenerationArchiveLimits{InstanceBytes: 1 << 20, OwnerBytes: 1 << 19, InstanceRecords: 16, OwnerRecords: 8}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.New(context.Background(), app.Options{Store: persistence.Receipts(receipts), Executor: f.executor, Archive: generation.PublicArchive(archive), Identity: authority.New(f.st.ResolvePrincipal), NewOutput: httpoutput.New,
		DigestKey: bytes.Repeat([]byte{47}, 32), Retention: time.Hour, Limits: model.ResponseRequestLimits{InstanceRecords: 32, OwnerRecords: 16}, ActiveRequests: 4, OwnerRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (f *responseFixture) request(key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	r = r.WithContext(access.WithPrincipal(r.Context(), f.principal))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.service.ServeHTTP(w, r)
	return w
}

func (f *responseFixture) receipt(t *testing.T, w *httptest.ResponseRecorder) model.ResponseRequest {
	t.Helper()
	var receipt model.ResponseRequest
	if err := f.st.DB().Where("id = ?", w.Header().Get("X-TXAPI-Generation-ID")).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestNativeResponsesExactReplayAndCanonicalRequestScope(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			f := newResponseFixture(t)
			streamFlag := "false"
			if stream {
				streamFlag = "true"
			}
			first := f.request("same-request", `{"model":"m","input":"private prompt text","stream":`+streamFlag+`,"max_output_tokens":16}`)
			replay := f.request("same-request", ` { "max_output_tokens":16, "stream":`+streamFlag+`,"input":"private prompt text", "model":"m" } `)
			if first.Code != 200 || replay.Code != 200 || first.Body.String() != replay.Body.String() || f.executor.calls.Load() != 1 {
				t.Fatalf("replay changed or executed twice: first=%d %s replay=%d %s calls=%d", first.Code, first.Body, replay.Code, replay.Body, f.executor.calls.Load())
			}
			for _, name := range []string{"X-TXAPI-Generation-ID", "X-TXAPI-Response-ID", "Content-Type"} {
				if first.Header().Get(name) == "" || first.Header().Get(name) != replay.Header().Get(name) {
					t.Fatalf("unstable replay header %s", name)
				}
			}
			if replay.Header().Get("X-TXAPI-Idempotency") != "replayed" || replay.Header().Get("X-Request-ID") != "" {
				t.Fatal("wrong replay delivery metadata")
			}
			if !strings.Contains(first.Body.String(), first.Header().Get("X-TXAPI-Response-ID")) || strings.Contains(first.Body.String(), "resp-private-upstream") {
				t.Fatal("public response identity was not retained")
			}
			receipt := f.receipt(t, first)
			if receipt.OutputRecordID == "" || receipt.State != model.ResponseCompleted || !receipt.Store {
				t.Fatalf("replay was not durably sealed: %+v", receipt)
			}
			metadata, _ := json.Marshal(receipt)
			if bytes.Contains(metadata, []byte("private prompt text")) || bytes.Contains(metadata, []byte("same-request")) {
				t.Fatal("plaintext request or idempotency key entered receipt metadata")
			}
			conflict := f.request("same-request", `{"model":"m","input":"different","stream":`+streamFlag+`,"max_output_tokens":16}`)
			if conflict.Code != 409 || f.executor.calls.Load() != 1 {
				t.Fatalf("different payload reused execution: %d %s", conflict.Code, conflict.Body)
			}
		})
	}
}

func TestResponsesStoreFalseRetainsOnlyReceiptAndNeverReexecutes(t *testing.T) {
	f := newResponseFixture(t)
	body := `{"model":"m","input":"private prompt text","store":false}`
	first := f.request("no-storage", body)
	replay := f.request("no-storage", body)
	if first.Code != 200 || replay.Code != 409 || f.executor.calls.Load() != 1 {
		t.Fatalf("store:false replay unexpectedly executed: %d %d", first.Code, replay.Code)
	}
	receipt := f.receipt(t, first)
	if receipt.Store || receipt.OutputRecordID != "" || receipt.State != model.ResponseCompleted {
		t.Fatalf("store:false retained output: %+v", receipt)
	}
	var count int64
	if err := f.st.DB().Model(&model.GenerationRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("store:false created archive records: %d %v", count, err)
	}
}

func TestResponsesConcurrentSameKeyReturnsConflictWithoutSecondExecution(t *testing.T) {
	f := newResponseFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	f.executor.before = func() { close(started); <-release }
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.request("concurrent", `{"model":"m","input":"hi"}`) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("execution did not start")
	}
	duplicate := f.request("concurrent", `{"model":"m","input":"hi"}`)
	close(release)
	select {
	case first := <-done:
		if first.Code != 200 || duplicate.Code != 409 || duplicate.Header().Get("Retry-After") == "" || f.executor.calls.Load() != 1 {
			t.Fatalf("duplicate dispatch: first=%d duplicate=%d calls=%d", first.Code, duplicate.Code, f.executor.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original execution failed to finish")
	}
}

func TestResponsesReplayRechecksCurrentAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed models", true: "revoked key"}[revoked], func(t *testing.T) {
			f := newResponseFixture(t)
			body := `{"model":"m","input":"hi"}`
			first := f.request("authority", body)
			if first.Code != 200 {
				t.Fatal(first.Body.String())
			}
			want := 403
			if revoked {
				if err := f.st.DeleteOwnedToken(f.binding.UserID, f.binding.TokenID); err != nil {
					t.Fatal(err)
				}
				want = 401
			} else {
				if _, err := f.st.UpdateOwnedToken(f.binding.UserID, f.binding.TokenID, &store.TokenInput{ModelMode: access.Allowlist, Models: []string{"other"}}); err != nil {
					t.Fatal(err)
				}
			}
			denied := f.request("authority", body)
			if denied.Code != want || strings.Contains(denied.Body.String(), "retained native answer") || f.executor.calls.Load() != 1 {
				t.Fatalf("stale authority replayed content: %d %s", denied.Code, denied.Body)
			}
		})
	}
}

func TestResponsesUnknownExecutionNeverReexecutes(t *testing.T) {
	f := newResponseFixture(t)
	f.executor.unknown = true
	body := `{"model":"m","input":"hi"}`
	first := f.request("unknown", body)
	replay := f.request("unknown", body)
	if replay.Code != 409 || !strings.Contains(replay.Body.String(), "idempotency_unknown") || f.executor.calls.Load() != 1 || f.receipt(t, first).State != model.ResponseUncertain {
		t.Fatalf("unknown execution was repeated: %d %s", replay.Code, replay.Body)
	}
}

type unacknowledgedCompletion struct{ *store.Store }

func (*unacknowledgedCompletion) FinishResponseRequest(context.Context, model.ResponseRequest, model.ResponseRequestCompletion) error {
	return errors.New("fixture crash before receipt finalization")
}

func TestResponsesRestartRecoveryProtectsDispatchingScope(t *testing.T) {
	f := newResponseFixture(t)
	f.service = f.newService(t, &unacknowledgedCompletion{f.st})
	body := `{"model":"m","input":"hi"}`
	first := f.request("restart", body)
	if f.receipt(t, first).State != model.ResponseDispatching {
		t.Fatal("fixture did not leave a dispatching receipt")
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.st, err = store.Open(config.DatabaseConfig{Driver: "sqlite", Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := f.st.RecoverResponseRequests(context.Background(), 16); err != nil || count != 1 {
		t.Fatalf("restart did not fence dispatching receipt: %d %v", count, err)
	}
	f.service = f.newService(t, f.st)
	replay := f.request("restart", body)
	if replay.Code != 409 || !strings.Contains(replay.Body.String(), "idempotency_unknown") || f.executor.calls.Load() != 1 {
		t.Fatalf("restart reexecuted unknown scope: %d %s", replay.Code, replay.Body)
	}
}

func TestResponsesCompleteNativeFailureCanReplay(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			f := newResponseFixture(t)
			f.executor.status = status
			body := `{"model":"m","input":"hi"}`
			first, replay := f.request("terminal", body), f.request("terminal", body)
			if first.Code != 200 || replay.Code != 200 || first.Body.String() != replay.Body.String() || f.executor.calls.Load() != 1 {
				t.Fatalf("native terminal result lost replay: %d %d %s", first.Code, replay.Code, replay.Body)
			}
			receipt := f.receipt(t, first)
			want := model.ResponseFailed
			if status == "incomplete" {
				want = model.ResponseCompleted
			}
			if receipt.NativeStatus != status || receipt.State != want || receipt.OutputRecordID == "" {
				t.Fatalf("wrong native terminal receipt: %+v", receipt)
			}
		})
	}
}
