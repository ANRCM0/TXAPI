package generation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/foundation/persistence"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	native "minigateway/internal/protocol/responses"
	"minigateway/internal/store"
)

func backgroundHistoryRuntimeSetup(t *testing.T, f bgRuntimeFixture) (history.Repository, execv1.TargetConstraint) {
	t.Helper()
	repo, err := NewResponseHistory(ResponseHistoryOptions{Store: persistence.History(f.st), Protector: f.s.keys, Retention: time.Hour,
		Limits: state.HistoryLimits{InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	f.s.history, f.s.compiler = repo, native.BackgroundHistoryCodec{}
	channels := f.st.ListChannels()
	if len(channels) != 1 || len(channels[0].Credentials) != 1 {
		t.Fatal("fixture must have one concrete target", channels)
	}
	ch, credential := channels[0], channels[0].Credentials[0]
	// This mirrors the registered static target identity; no test-only route or
	// unbound remote is used when the real runner dispatches the child.
	raw, _ := json.Marshal([]string{ch.Type, ch.BaseURL, credential.APIKey, "m"})
	sum := sha256.Sum256(raw)
	return repo, execv1.TargetConstraint{ChannelID: ch.ID, CredentialID: credential.ID, ActualModel: "m", Fingerprint: hex.EncodeToString(sum[:])}
}

func backgroundHistoryRuntimeParent(t *testing.T, f bgRuntimeFixture, repo history.Repository, target execv1.TargetConstraint, mutate func(*state.ResponseHistory, *history.Content)) state.ResponseHistory {
	t.Helper()
	binding := identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}
	row := repo.Fresh("resp_saved_parent", "response", "m", binding)
	content := history.Content{Ready: true, Target: target, Items: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"saved parent input"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"saved parent output"}]}`),
	}}
	if mutate != nil {
		mutate(&row, &content)
	}
	sealed, err := repo.Seal(row, content)
	if err != nil {
		t.Fatal(err)
	}
	binding = identity.Binding{UserID: row.UserID, TokenID: row.TokenID}
	if err = repo.Apply(f.ctx, binding, []state.HistoryChange{{Record: sealed}}); err != nil {
		t.Fatal(err)
	}
	return sealed
}

func backgroundHistoryRuntimeSubmission(n int, parent, input string) bg.Submit {
	body, _ := json.Marshal(map[string]any{"model": "m", "background": true, "stream": true, "store": false, "previous_response_id": parent, "input": input})
	return bg.Submit{ID: fmt.Sprintf("resp_%064x", n), Operation: execv1.Responses, Body: body, Stream: true, Store: false, IdempotencyKey: "saved-parent-request"}
}

func TestBackgroundHistoryRuntimeSnapshotSurvivesParentDeletionAndRestart(t *testing.T) {
	var posts atomic.Int64
	wire := make(chan []byte, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 400)
			return
		}
		posts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		wire <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, bgWire(0, "queued")+bgWire(1, "completed"))
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "history-restart.db")
	f := bgRuntimeOpen(t, path, upstream.URL, nil)
	repo, target := backgroundHistoryRuntimeSetup(t, f)
	parent := backgroundHistoryRuntimeParent(t, f, repo, target, nil)
	submission := backgroundHistoryRuntimeSubmission(7101, parent.ID, "child input")
	receipt, err := f.s.Submit(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.st.ReadBackground(f.ctx, identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := f.s.openPayload(row)
	if err != nil || payload.Target == nil || *payload.Target != target || payload.ParentID != parent.ID || payload.ParentVersion != parent.Version {
		t.Fatal("accepted task lost its authenticated parent snapshot", payload, err)
	}
	if err = f.st.DB().Where("id = ?", parent.ID).Delete(&model.ResponseHistory{}).Error; err != nil {
		t.Fatal(err)
	}
	submission.ID = fmt.Sprintf("resp_%064x", 7102)
	if duplicate, e := f.s.Submit(f.ctx, submission); e != nil || duplicate.ID != receipt.ID {
		t.Fatal("retry reread deleted parent", duplicate, e)
	}
	changed := backgroundHistoryRuntimeSubmission(7103, parent.ID, "different child input")
	if _, e := f.s.Submit(f.ctx, changed); !errors.Is(e, bg.ErrConflict) {
		t.Fatal("changed request reused receipt", e)
	}
	if err = f.st.Close(); err != nil {
		t.Fatal(err)
	}
	// The restarted service intentionally has neither History nor Compiler. Its
	// durable accepted input must execute without reconstructing the parent.
	f = bgRuntimeOpen(t, path, upstream.URL, nil)
	if err = f.runner.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("accepted task did not execute exactly once", posts.Load())
	}
	body := <-wire
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields["previous_response_id"] != nil || !bytes.Equal(body, payload.Body) {
		t.Fatalf("wire changed compiled snapshot: %s; accepted %s", body, payload.Body)
	}
	var input []json.RawMessage
	if json.Unmarshal(fields["input"], &input) != nil || len(input) != 3 || !bytes.Contains(input[0], []byte("saved parent input")) || !bytes.Contains(input[1], []byte("saved parent output")) || !bytes.Contains(input[2], []byte("child input")) {
		t.Fatal("wire lacks complete parent and child input", string(body))
	}
	completed, err := f.st.ReadBackground(f.ctx, identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}, receipt.ID)
	if err != nil || completed.Stage != string(bg.StageTerminal) {
		t.Fatal(completed, err)
	}
	snapshot, err := f.s.openSnapshot(completed)
	if err != nil || snapshot.Target != target {
		t.Fatal("worker changed parent target", snapshot.Target, err)
	}
	if err = f.runner.runOne(context.Background()); !errors.Is(err, bg.ErrNotFound) || posts.Load() != 1 {
		t.Fatal("completed task was sent twice", posts.Load(), err)
	}
	var count int64
	if err = f.st.DB().Model(&model.ResponseHistory{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("P6-A unexpectedly published a history row", count, err)
	}
}

func TestBackgroundHistoryRuntimeProcessDeathPreservesParentSnapshot(t *testing.T) {
	bgRuntimeCrashWindows(t, func(t *testing.T, f bgRuntimeFixture) bgRuntimeCrashScenario {
		repo, target := backgroundHistoryRuntimeSetup(t, f)
		parent := backgroundHistoryRuntimeParent(t, f, repo, target, nil)
		receipt, err := f.s.Submit(f.ctx, backgroundHistoryRuntimeSubmission(7151, parent.ID, "child after process death"))
		if err != nil {
			t.Fatal(err)
		}
		binding := identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}
		accepted, err := f.st.ReadBackground(f.ctx, binding, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := f.s.openPayload(accepted)
		if err != nil || payload.Target == nil || *payload.Target != target || payload.ParentID != parent.ID || payload.ParentVersion != parent.Version {
			t.Fatal("accepted parent snapshot is incomplete", payload, err)
		}
		if err = f.st.DB().Where("id = ?", parent.ID).Delete(&model.ResponseHistory{}).Error; err != nil {
			t.Fatal(err)
		}
		return bgRuntimeCrashScenario{
			receipt: receipt,
			validatePost: func(t *testing.T, body []byte) {
				if !bytes.Equal(body, payload.Body) {
					t.Errorf("crash/recovery changed accepted parent context: got %s; accepted %s", body, payload.Body)
				}
				var fields map[string]json.RawMessage
				var input []json.RawMessage
				if json.Unmarshal(body, &fields) != nil || fields["previous_response_id"] != nil || json.Unmarshal(fields["input"], &input) != nil || len(input) != 3 {
					t.Errorf("crash/recovery did not send complete local context: %s", body)
				}
				for _, text := range []string{"saved parent input", "saved parent output", "child after process death"} {
					if !bytes.Contains(body, []byte(text)) {
						t.Errorf("crash/recovery omitted context item %q", text)
					}
				}
			},
			validateRow: func(t *testing.T, recovered bgRuntimeFixture, row model.BackgroundJob) {
				if recovered.s.history != nil || recovered.s.compiler != nil {
					t.Fatal("recovery accidentally depended on live history services")
				}
				retained, err := recovered.st.ReadBackground(recovered.ctx, binding, row.PublicID)
				if err != nil {
					t.Fatal(err)
				}
				input, err := recovered.s.openPayload(retained)
				if err != nil || !bytes.Equal(input.Body, payload.Body) || input.Target == nil || *input.Target != target || input.ParentID != parent.ID || input.ParentVersion != parent.Version {
					t.Fatal("process death changed encrypted parent snapshot", input, err)
				}
				snapshot, err := recovered.s.openSnapshot(retained)
				if err != nil || snapshot.Target != target {
					t.Fatal("process death changed the dispatched continuity target", snapshot.Target, target, err)
				}
				var count int64
				if err := recovered.st.DB().Model(&model.ResponseHistory{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("process recovery published a new history row", count, err)
				}
			},
		}
	})
}

type backgroundHistoryRuntimeReadRace struct {
	history.Repository
	afterRead func() error
}

func (r backgroundHistoryRuntimeReadRace) Read(ctx context.Context, b identity.Binding, id string) (state.ResponseHistory, history.Content, error) {
	row, content, err := r.Repository.Read(ctx, b, id)
	if err == nil {
		err = r.afterRead()
	}
	return row, content, err
}

func TestBackgroundHistoryRuntimeRejectsInvalidParentsBeforeAcceptance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*state.ResponseHistory, *history.Content)
		want   error
	}{
		{"not-ready", func(_ *state.ResponseHistory, c *history.Content) { c.Ready = false }, bg.ErrConflict},
		{"busy", func(r *state.ResponseHistory, _ *history.Content) { r.Busy = "active_parent" }, bg.ErrConflict},
		{"active", func(_ *state.ResponseHistory, c *history.Content) { c.Active = "active_parent" }, bg.ErrConflict},
		{"partial-target", func(_ *state.ResponseHistory, c *history.Content) { c.Target.ActualModel = "" }, bg.ErrUnsupportedCapability},
		{"different-model", func(r *state.ResponseHistory, _ *history.Content) { r.Model = "other" }, bg.ErrConflict},
		{"missing", nil, bg.ErrNotFound},
		{"different-key", nil, bg.ErrNotFound},
		{"changed-after-read", nil, bg.ErrConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "parent-invalid.db"), "http://unused.test", nil)
			repo, target := backgroundHistoryRuntimeSetup(t, f)
			mutate := tc.mutate
			if tc.name == "different-key" {
				key, err := f.st.IssueToken(f.key.UserID, &store.TokenInput{ModelMode: "all"})
				if err != nil {
					t.Fatal(err)
				}
				mutate = func(r *state.ResponseHistory, _ *history.Content) { r.TokenID = key.ID }
			}
			parent := backgroundHistoryRuntimeParent(t, f, repo, target, mutate)
			if tc.name == "missing" {
				if err := f.st.DB().Where("id = ?", parent.ID).Delete(&model.ResponseHistory{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "changed-after-read" {
				f.s.history = backgroundHistoryRuntimeReadRace{Repository: repo, afterRead: func() error {
					return f.st.DB().Model(&model.ResponseHistory{}).Where("id = ?", parent.ID).Update("version", gorm.Expr("version + 1")).Error
				}}
			}
			if _, err := f.s.Submit(f.ctx, backgroundHistoryRuntimeSubmission(7201, parent.ID, "child")); !errors.Is(err, tc.want) {
				t.Fatal("invalid parent accepted or wrong rejection", err, tc.want)
			}
			var count int64
			if err := f.st.DB().Model(&model.BackgroundJob{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("invalid parent left a background task", count, err)
			}
		})
	}
}

func TestBackgroundHistoryRuntimeProgressPinsTargetAndRechecksRevocation(t *testing.T) {
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "target-fence.db"), "http://unused.test", nil)
	repo, target := backgroundHistoryRuntimeSetup(t, f)
	parent := backgroundHistoryRuntimeParent(t, f, repo, target, nil)
	if _, err := f.s.Submit(f.ctx, backgroundHistoryRuntimeSubmission(7301, parent.ID, "child")); err != nil {
		t.Fatal(err)
	}
	row, claim, err := f.st.ClaimBackground(f.ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := &backgroundProgress{service: f.s, row: row, claim: claim}
	payload, err := f.s.openPayload(row)
	if err != nil {
		t.Fatal(err)
	}
	call := bs.RemoteCall{Action: bg.RemoteSubmit, Operation: execv1.Responses, Model: row.Model, PublicID: row.PublicID, Stream: row.Stream, Body: payload.Body, Target: &target}
	if err = p.Validate(f.ctx, call); err != nil {
		t.Fatal("valid sealed call rejected", err)
	}
	missing := call
	missing.Target = nil
	if err = p.Validate(f.ctx, missing); !errors.Is(err, bg.ErrConflict) {
		t.Fatal("nil target bypassed validation", err)
	}
	if err = p.Dispatch(f.ctx, execv1.TargetConstraint{}, bs.RateReservation{}); !errors.Is(err, bg.ErrConflict) {
		t.Fatal("empty dispatch target accepted", err)
	}
	mutations := []func(*execv1.TargetConstraint){
		func(t *execv1.TargetConstraint) { t.ChannelID++ },
		func(t *execv1.TargetConstraint) { t.CredentialID++ },
		func(t *execv1.TargetConstraint) { t.ActualModel = "different" },
		func(t *execv1.TargetConstraint) { t.Fingerprint = "different" },
	}
	for _, mutate := range mutations {
		drifted := target
		mutate(&drifted)
		changed := call
		changed.Target = &drifted
		if err = p.Validate(f.ctx, changed); !errors.Is(err, bg.ErrConflict) {
			t.Fatal("target drift bypassed validation", drifted, err)
		}
		if err = p.Dispatch(f.ctx, drifted, bs.RateReservation{}); !errors.Is(err, bg.ErrConflict) {
			t.Fatal("target drift bypassed dispatch fence", drifted, err)
		}
	}
	if err = f.st.DB().Model(&model.Token{}).Where("id = ?", f.key.ID).Update("status", model.TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err = p.Dispatch(f.ctx, target, bs.RateReservation{}); !errors.Is(err, bg.ErrNotFound) {
		t.Fatal("dispatch used pre-revocation principal", err)
	}
	retained, err := f.st.ReadClaimedBackground(f.ctx, claim)
	if err != nil || retained.Stage != string(bg.StageQueued) || len(retained.Snapshot) != 0 {
		t.Fatal("rejected dispatch changed durable state", retained, err)
	}
}

func TestBackgroundHistoryRuntimeLegacyPayloadAndConstructorDependencies(t *testing.T) {
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "legacy-payload.db"), "http://unused.test", nil)
	receipt := f.submit(t, 7401)
	row, err := f.st.ReadBackground(f.ctx, identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := canonicalBackground([]byte(`{"model":"m","background":true,"stream":true,"store":false,"input":"private background input"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately encode the released two-field type, rather than computing
	// expected bytes or a fingerprint through the new production payload type.
	legacy, err := json.Marshal(struct {
		Operation execv1.Operation
		Body      json.RawMessage
	}{execv1.Responses, body})
	if err != nil {
		t.Fatal(err)
	}
	current, err := json.Marshal(backgroundPayload{Operation: execv1.Responses, Body: body})
	if err != nil || !bytes.Equal(legacy, current) {
		t.Fatal("new fields changed released payload bytes", string(legacy), string(current), err)
	}
	mac := hmac.New(sha256.New, bytes.Repeat([]byte{67}, 32))
	_, _ = mac.Write([]byte("TXAPI/background/v1/request\x00"))
	_, _ = mac.Write(legacy)
	if row.RequestHash != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("released request fingerprint changed", row.RequestHash)
	}
	sealed, err := f.s.keys.Seal(legacy, backgroundAAD(row, "input"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("ciphertext", sealed).Error; err != nil {
		t.Fatal(err)
	}
	options := BackgroundOptions{Repository: f.st, Parser: native.BackgroundParser{}, BillingMode: "disabled", ActiveKey: "event", Keys: map[string][]byte{"event": bytes.Repeat([]byte{61}, 32)}, DigestKey: bytes.Repeat([]byte{67}, 32), Retention: f.s.retention, MetadataRetention: f.s.metadataRetention, Limits: f.s.limits}
	restored, err := NewBackgroundService(f.ctx, options)
	if err != nil {
		t.Fatal("released payload could not restore without history dependencies", err)
	}
	f.s = restored
	if duplicate := f.submit(t, 7401); duplicate.ID != receipt.ID {
		t.Fatal("released receipt fingerprint no longer deduplicates", duplicate, receipt)
	}
	repo, _ := backgroundHistoryRuntimeSetup(t, f)
	options.History = repo
	if _, err = NewBackgroundService(f.ctx, options); !errors.Is(err, bg.ErrUnavailable) {
		t.Fatal("history without compiler accepted", err)
	}
	options.History, options.Compiler = nil, native.BackgroundHistoryCodec{}
	if _, err = NewBackgroundService(f.ctx, options); !errors.Is(err, bg.ErrUnavailable) {
		t.Fatal("compiler without history accepted", err)
	}
	options.History = repo
	if _, err = NewBackgroundService(f.ctx, options); err != nil {
		t.Fatal("paired history dependencies rejected old payload", err)
	}
}
