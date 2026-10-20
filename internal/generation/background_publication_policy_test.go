package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/model"
	native "minigateway/internal/protocol/responses"
)

func publicationPolicySubmit(n int, body string, store bool) bg.Submit {
	return bg.Submit{ID: fmt.Sprintf("resp_%064x", n), Operation: execv1.Responses, Body: []byte(body), Store: store, IdempotencyKey: fmt.Sprintf("publication-policy-%d", n)}
}

func publicationPolicyJob(t *testing.T, f bgRuntimeFixture, id string) model.BackgroundJob {
	t.Helper()
	var row model.BackgroundJob
	if err := f.st.DB().Where("public_id = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func publicationPolicyReservationReleased(t *testing.T, f bgRuntimeFixture, id string) {
	t.Helper()
	row := publicationPolicyJob(t, f, id)
	if row.Stage != string(bg.StageTerminal) {
		t.Fatal("publication rejection lost the known task terminal", row.Stage)
	}
	var saved model.ResponseHistory
	if err := f.st.DB().Where("id = ?", id).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.Deleted || saved.ReservedBytes != 0 || saved.Busy != "" || len(saved.Ciphertext) != 0 {
		t.Fatal("rejected publication retained reserved content or became readable", saved)
	}
}

func TestBackgroundPublicationPolicyOnlyExplicitSupportedStoreReserves(t *testing.T) {
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "policy.db"), "https://unused.test", nil)
	backgroundHistoryRuntimeSetup(t, f)
	f.s.projector = native.BackgroundHistoryCodec{}
	for index, test := range []struct {
		name, body  string
		store, want bool
	}{
		{"explicit true", `{"model":"m","background":true,"store":true,"input":"save"}`, true, true},
		{"omitted", `{"model":"m","background":true,"input":"temporary"}`, true, false},
		{"explicit false", `{"model":"m","background":true,"store":false,"input":"temporary"}`, false, false},
		{"unsupported custom", `{"model":"m","background":true,"store":true,"input":[{"type":"custom_tool_call","call_id":"custom","name":"edit","input":"patch"}]}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt, err := f.s.Submit(f.ctx, publicationPolicySubmit(8300+index, test.body, test.store))
			if err != nil {
				t.Fatal("existing background acceptance was narrowed", err)
			}
			binding := identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}
			row, err := f.st.ReadBackground(f.ctx, binding, receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := f.s.openPayload(row)
			if err != nil || payload.PublishHistory != test.want {
				t.Fatal("publication intent was inferred from the legacy Store field", payload.PublishHistory, test.want, err)
			}
			var rows []model.ResponseHistory
			if err := f.st.DB().Where("id = ?", receipt.ID).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if !test.want {
				if len(rows) != 0 {
					t.Fatal("temporary or unsupported input reserved permanent history")
				}
				return
			}
			if len(rows) != 1 || rows[0].Deleted || rows[0].Busy == "" || rows[0].ReservedBytes != state.ResponseHistoryMaxBytes {
				t.Fatal("explicit supported request did not reserve bounded history", rows)
			}
			_, content, err := f.s.history.Read(f.ctx, binding, receipt.ID)
			if err != nil || content.Ready || content.Active != rows[0].Busy {
				t.Fatal("reservation was prematurely published", content.Ready, err)
			}
		})
	}
}

func TestBackgroundPublicationPolicyOldIdempotentReceiptNeverBackfills(t *testing.T) {
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "legacy.db"), "https://unused.test", nil)
	backgroundHistoryRuntimeSetup(t, f)
	submission := publicationPolicySubmit(8401, `{"model":"m","background":true,"store":true,"input":"accepted before projector"}`, true)
	first, err := f.s.Submit(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	var original model.BackgroundJob
	if err := f.st.DB().First(&original, "public_id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Model(&original).Update("namespace_hash", "").Error; err != nil {
		t.Fatal(err)
	}
	f.s.projector = native.BackgroundHistoryCodec{}
	submission.ID = fmt.Sprintf("resp_%064x", 8402)
	again, err := f.s.Submit(f.ctx, submission)
	if err != nil || again.ID != first.ID {
		t.Fatal("old receipt changed identity when publication was enabled", again, err)
	}
	row, err := f.st.ReadBackground(f.ctx, identity.Binding{UserID: f.key.UserID, TokenID: f.key.ID}, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := f.s.openPayload(row)
	var count int64
	if err != nil || payload.PublishHistory {
		t.Fatal("old encrypted payload gained publication intent", payload.PublishHistory, err)
	}
	if err := f.st.DB().Model(&model.ResponseHistory{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("old idempotent receipt backfilled history", count, err)
	}
	var replayed model.BackgroundJob
	if err := f.st.DB().First(&replayed, "public_id = ?", first.ID).Error; err != nil || replayed.NamespaceHash == "" || replayed.Revision != original.Revision || string(replayed.Ciphertext) != string(original.Ciphertext) {
		t.Fatal("legacy namespace replay changed task identity or content", err)
	}
}

func TestBackgroundPublicationPolicyUnsaveableTerminalReleasesReservation(t *testing.T) {
	for _, test := range []struct {
		name, status, output string
	}{
		{"failed", "failed", `[]`},
		{"incomplete", "incomplete", `[]`},
		{"cancelled", "cancelled", `[]`},
		{"missing output", "completed", ""},
		{"null output", "completed", `null`},
		{"unsupported reasoning", "completed", `[{"type":"reasoning","id":"rs_provider","encrypted_content":"opaque"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					t.Error("unexpected provider operation", r.Method, r.URL)
				}
				document := map[string]any{"id": "resp_policy", "object": "response", "model": "m", "status": test.status}
				if test.output != "" {
					document["output"] = json.RawMessage(test.output)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(document)
			}))
			defer upstream.Close()
			f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "terminal.db"), upstream.URL, nil)
			backgroundHistoryRuntimeSetup(t, f)
			f.s.projector = native.BackgroundHistoryCodec{}
			receipt, err := f.s.Submit(f.ctx, publicationPolicySubmit(8501, `{"model":"m","background":true,"store":true,"input":"known input"}`, true))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.runner.runOne(context.Background()); err != nil {
				t.Fatal("history policy prevented terminal cleanup", err)
			}
			publicationPolicyReservationReleased(t, f, receipt.ID)
		})
	}
}

func TestBackgroundPublicationPolicyRevocationAfterReadCommitsOnlyTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_policy","object":"response","model":"m","status":"completed","output":[{"type":"message","id":"msg_provider","role":"assistant","content":[{"type":"output_text","text":"known result"}]}]}`)
	}))
	defer upstream.Close()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "revoked.db"), upstream.URL, nil)
	repo, _ := backgroundHistoryRuntimeSetup(t, f)
	f.s.projector = native.BackgroundHistoryCodec{}
	receipt, err := f.s.Submit(f.ctx, publicationPolicySubmit(8601, `{"model":"m","background":true,"store":true,"input":"known input"}`, true))
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	f.s.history = backgroundHistoryRuntimeReadRace{Repository: repo, afterRead: func() error {
		reads.Add(1)
		return f.st.DB().Model(&model.Token{}).Where("id = ?", f.key.ID).Update("status", model.TokenStatusDisabled).Error
	}}
	if err := f.runner.runOne(context.Background()); err != nil {
		t.Fatal("late revocation blocked bound terminal cleanup", err)
	}
	if reads.Load() != 1 {
		t.Fatal("test did not revoke between history read and task commit", reads.Load())
	}
	publicationPolicyReservationReleased(t, f, receipt.ID)
}
