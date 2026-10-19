package generation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/compat/responseshttp"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/responses"
)

// A background GET must not invoke either foreground capability.
type backgroundNullForeground struct {
	execv1.Executor
	state.Archive
}

func TestBackgroundPersistedDispatchWithoutDocumentRetrievesQueuedObject(t *testing.T) {
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "dispatch-document.db"), "http://unused.test", nil)
	receipt := f.submit(t, 901)
	row, claim, err := f.st.ClaimBackground(f.ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	progress := &backgroundProgress{service: f.s, row: row, claim: claim}
	target := execv1.TargetConstraint{ChannelID: 1, CredentialID: 1, Fingerprint: "dispatch-fixture"}
	if err := progress.Dispatch(f.ctx, target, bs.RateReservation{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.ReleaseBackground(f.ctx, claim, time.Now().Add(time.Hour).UnixMilli()) })
	// Read the encrypted bytes back from SQLite, rather than the in-memory
	// progress snapshot: nil RawMessage roundtrips through the legacy JSON null.
	persisted, err := f.st.ReadClaimedBackground(f.ctx, claim)
	if err != nil || persisted.Stage != string(bg.StageDispatching) || len(persisted.Snapshot) == 0 {
		t.Fatal("dispatch snapshot was not persisted", persisted, err)
	}
	plain, err := f.s.keys.Open(persisted.Snapshot, backgroundAAD(persisted, "snapshot"))
	if err != nil || !bytes.Contains(plain, []byte(`"Document":null`)) {
		t.Fatal("fixture must exercise the existing encrypted JSON format", string(plain), err)
	}
	read, err := f.s.Read(f.ctx, receipt.ID)
	if err != nil || read.Stage != bg.StageDispatching {
		t.Fatal("persisted dispatch read failed", read, err)
	}
	if len(read.Document) != 0 {
		t.Errorf("absent persisted dispatch document became content: %q", read.Document)
	}
	unused := &backgroundNullForeground{}
	history, err := responses.NewHistory(responses.HistoryOptions{Store: persistence.History(f.st), Protector: f.s.keys,
		Retention: time.Hour, Limits: state.HistoryLimits{InstanceRecords: 8, OwnerRecords: 4, InstanceBytes: 8 << 20, OwnerBytes: 4 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	application, err := responses.New(f.ctx, responses.Options{Background: f.s, Resources: f.s, History: history,
		Store: persistence.Receipts(f.st), Identity: identity.New(f.st.ResolvePrincipal), Executor: unused, Archive: unused,
		DigestKey: bytes.Clone(f.s.digest), Retention: time.Hour,
		Limits: state.ResponseRequestLimits{InstanceRecords: 8, OwnerRecords: 4}, ActiveRequests: 4, OwnerRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	handler := responseshttp.NewV2(application.V2(), func(w http.ResponseWriter) execv1.DeadlineOutput { return httpoutput.New(w) })
	get := func() *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/v1/responses/"+receipt.ID, nil).WithContext(f.ctx)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request)
		if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("background GET changed transport metadata", out.Code, out.Header(), out.Body.String())
		}
		return out
	}
	early := get()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(early.Body.Bytes(), &object); err != nil || object == nil || string(object["object"]) != `"response"` ||
		string(object["id"]) != `"`+receipt.ID+`"` || string(object["status"]) != `"queued"` || string(object["background"]) != "true" || string(object["output"]) != "[]" {
		t.Errorf("persisted dispatch GET must return a queued response object, got %s (decode %v)", early.Body.String(), err)
	}
	// An actual document keeps its original bytes after the same encrypted
	// persistence roundtrip; normal reads do not synthesize over real content.
	document := []byte(`{"id":"` + receipt.ID + `","object":"response","status":"in_progress","output":[],"background":true}`)
	snapshot := progress.snapshot
	snapshot.RemoteID, snapshot.Document = "resp_remote", document
	sealed, err := f.s.sealSnapshot(progress.row, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpdateBackground(f.ctx, claim, progress.row.Revision, bs.Update{Stage: bg.StageRemoteActive, Snapshot: sealed}); err != nil {
		t.Fatal(err)
	}
	read, err = f.s.Read(f.ctx, receipt.ID)
	if err != nil || !bytes.Equal(read.Document, document) {
		t.Fatal("actual persisted document changed", string(read.Document), err)
	}
	if actual := get(); !bytes.Equal(actual.Body.Bytes(), document) {
		t.Fatal("actual HTTP document was replaced", actual.Body.String())
	}
}
