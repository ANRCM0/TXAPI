package generation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	codec "minigateway/internal/compat/responsesws"
	"minigateway/internal/config"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
	"minigateway/internal/store"
)

type storedHistoryFixture struct {
	st         *store.Store
	path       string
	binding    identity.Binding
	repository history.Repository
	service    history.StoredRounds
	keys       map[string][]byte
	limits     state.HistoryLimits
}

func newStoredHistoryFixture(t *testing.T) *storedHistoryFixture {
	t.Helper()
	f := &storedHistoryFixture{path: filepath.Join(t.TempDir(), "stored-history.db"), keys: map[string][]byte{"old": bytes.Repeat([]byte{91}, 32)}, limits: state.HistoryLimits{InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20}}
	var err error
	f.st, err = store.Open(config.DatabaseConfig{Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	owner, err := f.st.CreateUser(&store.UserInput{Username: "stored-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.st.IssueToken(owner.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.binding = identity.Binding{UserID: owner.ID, TokenID: key.ID}
	if err := f.st.InitializeResponseRequests(context.Background(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	f.openOwner(t, "old")
	return f
}

func (f *storedHistoryFixture) openOwner(t *testing.T, active string) {
	t.Helper()
	ring, err := secrets.NewRing(active, f.keys)
	if err != nil {
		t.Fatal(err)
	}
	f.repository, err = generation.NewResponseHistory(generation.ResponseHistoryOptions{Store: persistence.History(f.st), Protector: ring, Retention: time.Hour, Limits: f.limits})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = generation.NewStoredResponseRounds(f.repository, codec.StoredCodec{})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *storedHistoryFixture) prepare(t *testing.T, parent string) history.StoredRound {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "private-question", "previous_response_id": parent})
	if parent == "" {
		body = json.RawMessage(`{"type":"response.create","model":"alias","store":true,"input":"private-question"}`)
	}
	round, err := f.service.Prepare(context.Background(), f.binding, history.StoredCreate{Model: "alias", PreviousID: parent, JSON: body})
	if err != nil {
		t.Fatal(err)
	}
	return round
}

var storedFixtureTarget = execv1.TargetConstraint{ChannelID: 3, CredentialID: 7, ActualModel: "actual", Fingerprint: "fixture-pin"}

func storedFixtureTerminal(status string) json.RawMessage {
	item := map[string]any{"id": "msg_native", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "private-answer"}}}
	response := map[string]any{"id": "resp_native_private", "object": "response", "model": "actual", "status": status, "output": []any{item}}
	body, _ := json.Marshal(map[string]any{"type": "response." + status, "response": response})
	return body
}

func TestStoredHistoryKnownTerminalRestartRotationAndIndependentBranches(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newStoredHistoryFixture(t)
			round := f.prepare(t, "")
			parent := round.Prepared().ID
			frame, err := round.Observe(context.Background(), storedFixtureTerminal(status), storedFixtureTarget)
			if err != nil || bytes.Contains(frame, []byte("resp_native_private")) {
				t.Fatal("known terminal failed", err)
			}
			if err := round.Finish(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if _, err := round.Observe(context.Background(), storedFixtureTerminal(status), storedFixtureTarget); !errors.Is(err, state.ErrHistoryConflict) {
				t.Fatal("terminal was appended twice", err)
			}
			if err := f.st.Close(); err != nil {
				t.Fatal(err)
			}
			f.st, err = store.Open(config.DatabaseConfig{Path: f.path})
			if err != nil {
				t.Fatal(err)
			}
			f.keys["new"] = bytes.Repeat([]byte{92}, 32)
			f.openOwner(t, "new")
			row, content, err := f.repository.Read(context.Background(), f.binding, parent)
			if err != nil || row.Version != 2 || row.Busy != "" || !content.Ready || content.Active != "" || content.Target != storedFixtureTarget || len(content.Items) != 2 || !bytes.Contains(content.Response, []byte(`"status":"`+status+`"`)) {
				t.Fatal("restart lost known history", row, content, err)
			}
			for i := 0; i < 2; i++ {
				branch := f.prepare(t, parent)
				prepared := branch.Prepared()
				if prepared.Target == nil || *prepared.Target != storedFixtureTarget || !bytes.Contains(prepared.JSON, []byte("private-answer")) {
					t.Fatal("branch lost target or full context")
				}
				if err := branch.Finish(context.Background(), false); err != nil {
					t.Fatal(err)
				}
			}
			current, _, err := f.repository.Read(context.Background(), f.binding, parent)
			if err != nil || current.Version != 2 || current.Busy != "" {
				t.Fatal("independent branches mutated parent", current, err)
			}
			other, err := f.st.IssueToken(f.binding.UserID, &store.TokenInput{ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.repository.Read(context.Background(), identity.Binding{UserID: f.binding.UserID, TokenID: other.ID}, parent); !errors.Is(err, state.ErrHistoryNotFound) {
				t.Fatal("cross Key read permitted", err)
			}
		})
	}
}

func TestStoredHistoryUnknownRecoveryAndTerminalPersistenceFailure(t *testing.T) {
	f := newStoredHistoryFixture(t)
	round := f.prepare(t, "")
	id := round.Prepared().ID
	name := "test_reject_stored_terminal"
	if err := f.st.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "response_histories" {
			tx.AddError(errors.New("fixture write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if frame, err := round.Observe(context.Background(), storedFixtureTerminal("completed"), storedFixtureTarget); err == nil || len(frame) != 0 {
		t.Fatal("terminal escaped a failed persistent transaction")
	}
	f.st.DB().Callback().Update().Remove(name)
	row, content, err := f.repository.Read(context.Background(), f.binding, id)
	if err != nil || row.Version != 1 || row.Busy == "" || content.Ready {
		t.Fatal("failed terminal partially committed", row, content, err)
	}
	if err := round.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	f.st, err = store.Open(config.DatabaseConfig{Path: f.path})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecoverResponseHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.openOwner(t, "old")
	// Tampering with the plaintext busy column must not reopen unknown history:
	// the authenticated content carries its own uncertain marker.
	if err := f.st.DB().Model(&model.ResponseHistory{}).Where("id = ?", id).Update("busy", "").Error; err != nil {
		t.Fatal(err)
	}
	_, content, err = f.repository.Read(context.Background(), f.binding, id)
	if err != nil || content.Active != "uncertain" || content.Ready || content.Request != nil {
		t.Fatal("unknown content did not remain sealed", content, err)
	}
	_, err = f.service.Prepare(context.Background(), f.binding, history.StoredCreate{Model: "alias", PreviousID: id, JSON: json.RawMessage(`{"type":"response.create","model":"alias","store":true,"input":"do-not-repeat"}`)})
	if !errors.Is(err, state.ErrHistoryConflict) {
		t.Fatal("unknown generation was used as a parent", err)
	}
}

func TestStoredHistoryCapacityAndRevokedKeyRefusePublication(t *testing.T) {
	f := newStoredHistoryFixture(t)
	f.limits.OwnerRecords = 1
	f.openOwner(t, "old")
	round := f.prepare(t, "")
	if _, err := round.Observe(context.Background(), storedFixtureTerminal("completed"), storedFixtureTarget); err != nil {
		t.Fatal(err)
	}
	if err := round.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Prepare(context.Background(), f.binding, history.StoredCreate{Model: "alias", PreviousID: round.Prepared().ID, JSON: json.RawMessage(`{"type":"response.create","model":"alias","store":true,"input":"over-capacity"}`)})
	if !errors.Is(err, state.ErrHistoryCapacity) {
		t.Fatal("history capacity was bypassed", err)
	}
	var count int64
	if err := f.st.DB().Model(&model.ResponseHistory{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("failed capacity left a row", count, err)
	}
	f.limits.OwnerRecords = 16
	f.openOwner(t, "old")
	active := f.prepare(t, "")
	if err := f.st.DeleteToken(f.binding.TokenID); err != nil {
		t.Fatal(err)
	}
	if frame, err := active.Observe(context.Background(), storedFixtureTerminal("completed"), storedFixtureTarget); err == nil || len(frame) != 0 {
		t.Fatal("revoked Key published history")
	}
	var row model.ResponseHistory
	if err := f.st.DB().First(&row, "id = ?", active.Prepared().ID).Error; err != nil || row.Version != 1 || row.Busy == "" {
		t.Fatal("revoked finalization altered durable state", row, err)
	}
}
