package store

import (
	"context"
	"reflect"
	"testing"

	"minigateway/internal/config"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/model"
)

// This exact pre-v20 JSON shape must keep old admission fingerprints valid.
type oldUsageStart struct {
	ID               string
	RequestID        string
	Model            string
	QuotationVersion string
	Operation        v1.Operation
	Stream           bool
	MaxAttempts      int
}

func TestUsageTransportV20UpgradePreservesHTTPReplayAndProtocolReceipts(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(51)
	if err := r.Begin(f.ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := r.Prepare(f.ctx, usageTarget(start.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.MayDispatch(f.ctx, start.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(f.ctx, history.Receipt{ID: start.ID, Number: 1, Host: usageHost(), Usage: usageUnknown()}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(f.ctx, history.Completion{ID: start.ID, Host: usageHost()}); err != nil {
		t.Fatal(err)
	}
	var before model.UsageInvocation
	var receipt model.UsageAttemptReceipt
	if err := f.st.DB().First(&before, "id = ?", start.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().First(&receipt, "invocation_id = ?", start.ID).Error; err != nil {
		t.Fatal(err)
	}
	old := oldUsageStart{start.ID, start.RequestID, start.Model, start.QuotationVersion, start.Operation, start.Stream, start.MaxAttempts}
	if want := usageDigest(struct {
		UserID, TokenID uint
		Start           oldUsageStart
	}{f.owner.ID, f.token, old}); before.StartFingerprint != want {
		t.Fatal("changed the pre-v20 Start fingerprint")
	}
	for _, column := range []struct {
		table any
		name  string
	}{{&model.UsageInvocation{}, "transport"}, {&model.UsageInvocation{}, "host_protocol_status"}, {&model.UsageAttemptReceipt{}, "host_protocol_status"}} {
		if err := f.st.DB().Migrator().DropColumn(column.table, column.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.DB().Where("version > ?", 19).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	if err := f.st.DB().Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	var database struct{ File string }
	if err := f.st.DB().Raw("PRAGMA database_list").Scan(&database).Error; err != nil || database.File == "" {
		t.Fatal(database, err)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		st, err := Open(config.DatabaseConfig{Path: database.File})
		if err != nil {
			t.Fatal(err)
		}
		var after model.UsageInvocation
		var afterReceipt model.UsageAttemptReceipt
		if err := st.DB().First(&after, "id = ?", start.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.DB().First(&afterReceipt, "invocation_id = ?", start.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(receipt, afterReceipt) {
			t.Fatal("v20 changed prior facts or digests", after, afterReceipt)
		}
		start.Transport = "http"
		if err := st.UsageRecorder(usageLimits()).Begin(f.ctx, start); err != nil {
			t.Fatal("explicit HTTP did not replay old admission", err)
		}
		start.Transport = ""
		if err := st.UsageRecorder(usageLimits()).Begin(f.ctx, start); err != nil {
			t.Fatal("empty HTTP did not replay old admission", err)
		}
		r := st.UsageRecorder(usageLimits())
		if err := r.Observe(f.ctx, history.Receipt{ID: start.ID, Number: 1, Host: usageHost(), Usage: usageUnknown()}); err != nil {
			t.Fatal("old observed receipt did not replay", err)
		}
		if err := r.Finish(f.ctx, history.Completion{ID: start.ID, Host: usageHost()}); err != nil {
			t.Fatal("old completion did not replay", err)
		}
		var ledger []model.SchemaMigration
		if err := st.DB().Order("version").Find(&ledger).Error; err != nil || len(ledger) != len(released)+3 || ledger[len(ledger)-3].Version != 20 || ledger[len(ledger)-2].Version != 21 || ledger[len(ledger)-1].Version != 22 {
			t.Fatal(ledger, err)
		}
		if !reflect.DeepEqual(released, ledger[:len(released)]) {
			t.Fatal("changed released migration timestamps")
		}
		if err := checkMigrationVersion(st.DB(), 19); err == nil {
			t.Fatal("v19 binary accepted v20 database")
		}
		detail, err := st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.session}, start.ID)
		if err != nil || detail.Record.Transport != "http" || detail.Record.Host.ProtocolStatus != "" {
			t.Fatal(detail, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsageTransportValidationScopesAndUnknownProtocolState(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	valid := usageStart(52)
	valid.Transport = "websocket"
	valid.Stream = true
	valid.MaxAttempts = 1
	for _, change := range []func(*history.Start){func(s *history.Start) { s.Transport = "background" }, func(s *history.Start) { s.Stream = false }, func(s *history.Start) { s.Operation = v1.Chat }, func(s *history.Start) { s.MaxAttempts = 2 }} {
		invalid := valid
		change(&invalid)
		if err := r.Begin(f.ctx, invalid); err != history.ErrInvalid {
			t.Fatal("invalid transport admitted", invalid, err)
		}
	}
	if err := r.Begin(f.ctx, valid); err != nil {
		t.Fatal(err)
	}
	if err := r.Prepare(f.ctx, usageTarget(valid.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.MayDispatch(f.ctx, valid.ID, 1); err != nil {
		t.Fatal(err)
	}
	host := history.HostFacts{Phase: v1.ResponseReceived, Termination: v1.Canceled, ProtocolStatus: "in_progress", DeliveryCommitted: true}
	invalidHost := host
	invalidHost.ProtocolStatus = "private-arbitrary-status"
	if err := r.Observe(f.ctx, history.Receipt{ID: valid.ID, Number: 1, Host: invalidHost, Usage: usageUnknown()}); err != history.ErrInvalid {
		t.Fatal(err)
	}
	if err := r.Observe(f.ctx, history.Receipt{ID: valid.ID, Number: 1, Host: host, Usage: usageUnknown()}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(f.ctx, history.Completion{ID: valid.ID, Host: host}); err != nil {
		t.Fatal(err)
	}
	repo := f.st.UsageHistory(true, 3600)
	detail, err := repo.Read(context.Background(), history.Access{Session: f.session}, valid.ID)
	if err != nil || detail.Record.Transport != "websocket" || detail.Record.Host.ProtocolStatus != "in_progress" || detail.Attempts[0].Usage.TotalTokens != nil || detail.Attempts[0].Host.HTTPStatus != 0 {
		t.Fatal(detail, err)
	}
	if _, err := repo.Read(context.Background(), history.Access{Session: f.otherSession}, valid.ID); err != history.ErrNotFound {
		t.Fatal("cross-owner transport exposed", err)
	}
	page, err := repo.List(context.Background(), history.Access{Session: f.session}, history.Query{TokenID: f.token})
	if err != nil || len(page.Data) != 1 || page.Data[0].Transport != "websocket" {
		t.Fatal(page, err)
	}
	if _, err := repo.Read(context.Background(), history.Access{Session: f.session, Administrator: true}, valid.ID); err != history.ErrForbidden {
		t.Fatal("owner escalated to admin", err)
	}
}

func TestUsageWebSocketDispatchInterruptionRetainsTransportAndUnknownCounts(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(53)
	start.Transport = "websocket"
	start.Stream = true
	start.MaxAttempts = 1
	if err := r.Begin(f.ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := r.Prepare(f.ctx, usageTarget(start.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.MayDispatch(f.ctx, start.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.st.InterruptUsageHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	detail, err := f.st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.session}, start.ID)
	if err != nil || detail.Record.Transport != "websocket" || detail.Record.State != "interrupted" || detail.Record.Host.Phase != v1.Dispatching || detail.Record.Host.ProtocolStatus != "" || len(detail.Attempts) != 1 || !detail.Attempts[0].MayDispatch || detail.Attempts[0].Usage.TotalTokens != nil {
		t.Fatal("interrupted round invented known generation", detail, err)
	}
	if err := r.Begin(f.ctx, start); err != nil {
		t.Fatal("same observation replay lost idempotency", err)
	}
	start.Transport = "http"
	if err := r.Begin(f.ctx, start); err != history.ErrConflict {
		t.Fatal("transport collision replayed", err)
	}
}
