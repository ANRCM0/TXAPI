package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/config"
	bg "minigateway/internal/foundation/contracts/background/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"minigateway/internal/model"
)

func TestBackgroundUsageV21UpgradePreservesOpaqueTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v20-background.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	row := model.BackgroundJob{ID: strings.Repeat("a", 64), PublicID: "resp_" + strings.Repeat("b", 64), UserID: 1, TokenID: 2, Operation: "responses", Model: "m", KeyHash: strings.Repeat("c", 64), RequestHash: strings.Repeat("d", 64), DigestVersion: strings.Repeat("e", 64), Stage: "terminal", Stream: true, Revision: 7, ClaimEpoch: 3, LastSequence: 0, EventCount: 1, Ciphertext: []byte("opaque-v20-input"), Snapshot: []byte("opaque-v1-snapshot"), CreatedAt: time.Now().UnixMilli(), ContentExpiresAt: time.Now().Add(time.Minute).UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	event := model.BackgroundEvent{TaskID: row.ID, Sequence: 0, Ciphertext: []byte("opaque-v20-event")}
	for _, item := range []any{&row, &event} {
		if err = st.DB().Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = st.DB().Where("version >= 21").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"native_state", "usage_input_tokens", "usage_output_tokens", "usage_total_tokens", "usage_cached_input_tokens", "usage_cache_write_input_tokens", "usage_reasoning_output_tokens", "usage_completeness", "usage_source", "usage_profile"} {
		if err = st.DB().Migrator().DropColumn(&model.BackgroundJob{}, column); err != nil {
			t.Fatal(column, err)
		}
	}
	var released []model.SchemaMigration
	if err = st.DB().Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var got model.BackgroundJob
		var gotEvent model.BackgroundEvent
		if err = st.DB().First(&got).Error; err != nil || !reflect.DeepEqual(got, row) {
			t.Fatal("upgrade invented quantities or changed protected facts", got, err)
		}
		if err = st.DB().First(&gotEvent).Error; err != nil || !reflect.DeepEqual(gotEvent, event) {
			t.Fatal("upgrade rewrote retained events", gotEvent, err)
		}
		var ledger []model.SchemaMigration
		if err = st.DB().Order("version").Find(&ledger).Error; err != nil || len(ledger) != len(released)+2 || ledger[len(ledger)-2].Version != 21 || ledger[len(ledger)-1].Version != 22 {
			t.Fatal("v21 did not append exactly once", ledger, err)
		}
		if !reflect.DeepEqual(ledger[:len(released)], released) || checkMigrationVersion(st.DB(), 20) == nil {
			t.Fatal("released migrations or downgrade refusal changed")
		}
		if err = st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackgroundUsageSessionProjectionDoesNotReadContent(t *testing.T) {
	f := taskTestFixture(t)
	row := f.row(t, 1, f.owner.ID, "terminal")
	input, output, total, cached, reasoning := int64(9007199254740993), int64(1), int64(9007199254740994), int64(2), int64(0)
	row.NativeState = string(bg.RemoteIncomplete)
	row.Usage = model.BackgroundUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total, CachedInputTokens: &cached, ReasoningOutputTokens: &reasoning, Completeness: "complete", Source: "provider_reported", Profile: "responses_native_tokens_v2"}
	if err := f.st.DB().Save(&row).Error; err != nil {
		t.Fatal(err)
	}
	// Queries must succeed even when the content is invalid ciphertext, and must
	// not load that content as an implementation shortcut for extracting usage.
	const name = "background_usage_forbid_content"
	if err := f.st.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "background_jobs" {
			selection := strings.Join(tx.Statement.Selects, ",")
			if selection == "" || strings.Contains(selection, "snapshot") || strings.Contains(selection, "ciphertext") {
				tx.AddError(errors.New("metadata selected private content"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.st.DB().Callback().Query().Remove(name)
	ctx := context.Background()
	repo := f.st.TaskControl()
	for _, scope := range []task.Access{{Session: f.session}, {Session: f.adminSession, Administrator: true}} {
		got, err := repo.Read(ctx, scope, row.PublicID)
		if err != nil || got.ProtocolStatus != "incomplete" || got.Usage == nil || *got.Usage.InputTokens != "9007199254740993" || *got.Usage.TotalTokens != "9007199254740994" || *got.Usage.ReasoningOutputTokens != "0" || got.Usage.CacheWriteInputTokens != nil {
			t.Fatal("metadata quantities rounded or invented", got, err)
		}
		page, err := repo.List(ctx, scope, task.Query{})
		if err != nil || len(page.Data) != 1 || !reflect.DeepEqual(page.Data[0].Usage, got.Usage) {
			t.Fatal(page, err)
		}
		data, _ := json.Marshal(page)
		for _, secret := range []string{"sentinel", "ciphertext", "snapshot", "claim_hash", "channel_id", "credential"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("private content leaked", string(data))
			}
		}
	}
	if _, err := repo.Read(ctx, task.Access{Session: f.otherSession}, row.PublicID); !errors.Is(err, task.ErrNotFound) {
		t.Fatal("foreign owner learned usage", err)
	}
	if _, err := repo.Read(ctx, task.Access{Session: "sk-not-a-login"}, row.PublicID); !errors.Is(err, task.ErrUnauthorized) {
		t.Fatal("call key gained task usage", err)
	}
}
