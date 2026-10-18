package responses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/compat/responseshttp"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/secrets"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type historyExecutor struct {
	mu                sync.Mutex
	calls             int
	bodies            [][]byte
	targets           []*execv1.TargetConstraint
	before            func()
	unknown, panicNow bool
	output            string
}

func (e *historyExecutor) Execute(ctx context.Context, out execv1.Output, in execv1.Invocation) execv1.Outcome {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.bodies = append(e.bodies, bytes.Clone(in.Body))
	e.targets = append(e.targets, in.Target)
	before := e.before
	e.mu.Unlock()
	if before != nil {
		before()
	}
	if e.panicNow {
		panic("fixture interruption")
	}
	output := e.output
	if output == "" {
		output = fmt.Sprintf(`[{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"private-answer-%d"}]}]`, n, n)
	}
	var input struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(in.Body, &input)
	body := fmt.Sprintf(`{"id":%q,"object":"response","status":"completed","model":"m","output":%s,"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`, in.OutputID, output)
	contentType := "application/json"
	if input.Stream {
		contentType = "text/event-stream"
		body = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n"
	}
	result := execv1.Outcome{Operation: protocol.Responses, Native: execv1.NativeCompletion{Status: "completed", ID: "resp_upstream_private"}, Target: execv1.TargetConstraint{ChannelID: 1, CredentialID: 2, ActualModel: "native-m", Fingerprint: "fixture-stable-target"}, Result: execv1.Result{StatusCode: 200, Phase: execv1.ResponseReceived, Termination: execv1.Success, ProtocolComplete: !e.unknown}}
	if err := out.Commit(ctx, execv1.ResponseMetadata{StatusCode: 200, ContentType: contentType, Values: map[string]string{"X-TXAPI-Price-Version": "fixture-price"}}); err != nil {
		result.ProtocolComplete = false
		return result
	}
	if _, err := out.Write(ctx, []byte(body)); err != nil {
		result.ProtocolComplete = false
		result.Termination = execv1.ClientWriteError
	}
	if input.Stream {
		_ = out.Flush(ctx)
	}
	return result
}

func historyService(t *testing.T, f *responseFixture, e app.Executor, active string, keys map[string][]byte) http.Handler {
	t.Helper()
	archive, err := generation.NewArchive(generation.ArchiveOptions{Store: persistence.Archive(f.st), Resolve: f.st.ResolvePrincipal, ActiveKey: active, Keys: keys, Retention: time.Hour, MaxRecordBytes: 65536, Limits: model.GenerationArchiveLimits{InstanceBytes: 4 << 20, OwnerBytes: 2 << 20, InstanceRecords: 64, OwnerRecords: 32}})
	if err != nil {
		t.Fatal(err)
	}
	protector, err := secrets.NewRing(active, keys)
	if err != nil {
		t.Fatal(err)
	}
	history, err := app.NewHistory(app.HistoryOptions{Store: persistence.History(f.st), Protector: protector, Retention: time.Hour, Limits: model.HistoryLimits{InstanceRecords: 64, OwnerRecords: 32, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := app.New(context.Background(), app.Options{History: history, Store: persistence.Receipts(f.st), Executor: e, Archive: generation.PublicArchive(archive), Identity: authority.New(f.st.ResolvePrincipal), DigestKey: bytes.Repeat([]byte{47}, 32), Retention: time.Hour, Limits: model.ResponseRequestLimits{InstanceRecords: 64, OwnerRecords: 32}, ActiveRequests: 8, OwnerRequests: 4})
	if err != nil {
		t.Fatal(err)
	}
	return responseshttp.New(s, httpoutput.New)
}

func historyCall(s http.Handler, p access.Principal, method, path, body, key, match string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(access.WithPrincipal(r.Context(), p))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func objectID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var o struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &o) != nil || o.ID == "" {
		t.Fatal(w.Body.String())
	}
	return o.ID
}

func TestHistoryNativeJSONSSEContinuationRestartPrivacyAndDeletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newResponseFixture(t)
			e := &historyExecutor{}
			keys := map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)}
			s := historyService(t, f, e, "old", keys)
			body := fmt.Sprintf(`{"model":"m","input":"private-first-input","stream":%t}`, stream)
			first := historyCall(s, f.principal, "POST", "/v1/responses", body, "first", "")
			if first.Code != 200 {
				t.Fatal(first.Body)
			}
			id := first.Header().Get("X-TXAPI-Response-ID")
			get := historyCall(s, f.principal, "GET", "/v1/responses/"+id, "", "", "")
			if objectID(t, get) != id || !strings.Contains(get.Body.String(), "private-answer-1") {
				t.Fatal(get.Body)
			}
			items := historyCall(s, f.principal, "GET", "/v1/responses/"+id+"/input_items?limit=1", "", "", "")
			if items.Code != 200 || !strings.Contains(items.Body.String(), "private-first-input") {
				t.Fatal(items.Body)
			}
			var rows []model.ResponseHistory
			if err := f.st.DB().Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if bytes.Contains(r.Ciphertext, []byte("private-first-input")) || bytes.Contains(r.Ciphertext, []byte("private-answer")) {
					t.Fatal("plaintext history")
				}
			}
			if err := f.st.RecoverResponseHistory(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.st.Close(); err != nil {
				t.Fatal(err)
			}
			var reopenErr error
			f.st, reopenErr = store.Open(config.DatabaseConfig{Path: f.path})
			if reopenErr != nil {
				t.Fatal(reopenErr)
			}
			s = historyService(t, f, e, "new", map[string][]byte{"new": bytes.Repeat([]byte{53}, 32), "old": keys["old"]})
			childBody := fmt.Sprintf(`{"model":"m","input":"second","previous_response_id":%q}`, id)
			child := historyCall(s, f.principal, "POST", "/v1/responses", childBody, "child", "")
			childID := objectID(t, child)
			if !strings.Contains(child.Body.String(), `"previous_response_id":"`+id+`"`) {
				t.Fatal("live parent identity omitted")
			}
			var expanded map[string]json.RawMessage
			_ = json.Unmarshal(e.bodies[1], &expanded)
			if expanded["previous_response_id"] != nil || expanded["conversation"] != nil || !bytes.Contains(expanded["input"], []byte("private-first-input")) || !bytes.Contains(expanded["input"], []byte("private-answer-1")) || e.targets[1] == nil {
				t.Fatalf("wrong context: %s", e.bodies[1])
			}
			branch := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"branch","previous_response_id":%q}`, id), "branch", "")
			if branch.Code != 200 {
				t.Fatal(branch.Body)
			}
			if bytes.Contains(e.bodies[2], []byte("second")) {
				t.Fatal("sibling branch context contaminated")
			}
			other, err := f.st.IssueToken(f.binding.UserID, &store.TokenInput{ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.st.ResolvePrincipal(context.Background(), access.Binding{UserID: f.binding.UserID, TokenID: other.ID})
			if err != nil {
				t.Fatal(err)
			}
			if w := historyCall(s, p, "GET", "/v1/responses/"+childID, "", "", ""); w.Code != 404 {
				t.Fatalf("same-user other key: %d", w.Code)
			}
			if w := historyCall(s, f.principal, "DELETE", "/v1/responses/"+id, "", "", ""); w.Code != 200 {
				t.Fatal(w.Body)
			}
			if w := historyCall(s, f.principal, "GET", "/v1/responses/"+id, "", "", ""); w.Code != 404 {
				t.Fatal("deleted response visible")
			}
			if w := historyCall(s, f.principal, "POST", "/v1/responses", body, "first", ""); w.Code != 409 {
				t.Fatalf("deleted result replayed: %d", w.Code)
			}
			if w := historyCall(s, f.principal, "GET", "/v1/responses/"+childID, "", "", ""); w.Code != 200 {
				t.Fatal("deleting parent removed independent child")
			}
			if e.calls != 3 {
				t.Fatalf("read/delete/replay executed: %d", e.calls)
			}
			w := historyCall(s, f.principal, "POST", "/v1/responses", `{"model":"m","input":"ephemeral","store":false}`, "ephemeral", "")
			rid := objectID(t, w)
			if got := historyCall(s, f.principal, "GET", "/v1/responses/"+rid, "", "", ""); got.Code != 404 {
				t.Fatal("store:false retained public history")
			}
		})
	}
}

func TestConversationItemsCASConcurrentTurnAndUnknownRecovery(t *testing.T) {
	f := newResponseFixture(t)
	e := &historyExecutor{}
	s := historyService(t, f, e, "old", map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)})
	created := historyCall(s, f.principal, "POST", "/v1/conversations", `{"metadata":{"topic":"private-topic"},"items":[{"role":"user","content":"seed"}]}`, "", "")
	id := objectID(t, created)
	update := historyCall(s, f.principal, "POST", "/v1/conversations/"+id, `{"metadata":{"topic":"changed"}}`, "", created.Header().Get("ETag"))
	if update.Code != 200 {
		t.Fatal(update.Body)
	}
	if w := historyCall(s, f.principal, "POST", "/v1/conversations/"+id, `{"metadata":{}}`, "", created.Header().Get("ETag")); w.Code != 409 {
		t.Fatal("stale revision accepted")
	}
	appendResult := historyCall(s, f.principal, "POST", "/v1/conversations/"+id+"/items", `{"items":[{"role":"user","content":"extra"}]}`, "", "")
	if appendResult.Code != 200 {
		t.Fatal(appendResult.Body)
	}
	latest := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items?limit=1", "", "", "")
	if latest.Code != 200 || !strings.Contains(latest.Body.String(), "extra") {
		t.Fatal("default item order is not descending")
	}
	page := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items?limit=1&order=asc", "", "", "")
	var parsed struct {
		Last string `json:"last_id"`
		More bool   `json:"has_more"`
	}
	_ = json.Unmarshal(page.Body.Bytes(), &parsed)
	if page.Code != 200 || !parsed.More {
		t.Fatal(page.Body)
	}
	if w := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items?order=asc&after="+parsed.Last, "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "extra") {
		t.Fatal(w.Body)
	}
	if w := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items/"+parsed.Last, "", "", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := historyCall(s, f.principal, "DELETE", "/v1/conversations/"+id+"/items/"+parsed.Last, "", "", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	e.before = func() { close(entered); <-release }
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"turn","conversation":%q}`, id), "turn", "")
	}()
	<-entered
	if w := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"other","conversation":%q}`, id), "competing", ""); w.Code != 409 {
		t.Fatalf("concurrent append %d %s", w.Code, w.Body)
	}
	if w := historyCall(s, f.principal, "DELETE", "/v1/conversations/"+id, "", "", ""); w.Code != 409 {
		t.Fatal("active conversation deleted")
	}
	close(release)
	if w := <-done; w.Code != 200 {
		t.Fatal(w.Body)
	}
	e.before = nil
	if w := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items", "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "private-answer-1") {
		t.Fatal(w.Body)
	}
	e.unknown = true
	if w := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"unknown","conversation":%q}`, id), "unknown", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := f.st.RecoverResponseHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"unsafe","conversation":%q}`, id), "unsafe", ""); w.Code != 409 {
		t.Fatal("unknown conversation reused")
	}
	if w := historyCall(s, f.principal, "DELETE", "/v1/conversations/"+id, "", "", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if e.calls != 2 {
		t.Fatalf("concurrent/unknown guard dispatched: %d", e.calls)
	}
}
