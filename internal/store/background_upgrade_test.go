package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestBackgroundV14UpgradePreservesV13StateWithoutInventingDeletionOrNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v13-background.db")
	st, e := Open(config.DatabaseConfig{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	row := model.BackgroundJob{ID: strings.Repeat("a", 64), PublicID: "resp_" + strings.Repeat("b", 64), UserID: 1, TokenID: 2, Operation: "responses", Model: "m", KeyHash: strings.Repeat("c", 64), RequestHash: strings.Repeat("d", 64), DigestVersion: strings.Repeat("e", 64), Stage: "remote_active", Revision: 7, ClaimEpoch: 3, ClaimHash: strings.Repeat("f", 64), ClaimUntil: 91, LastSequence: 4, StoredBytes: 31, EventCount: 5, Ciphertext: []byte("opaque-v13-input"), Snapshot: []byte("opaque-v13-binding"), CreatedAt: time.Now().UnixMilli(), ContentExpiresAt: time.Now().Add(time.Minute).UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if e = st.DB().Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.DB().Where("version >= 14").Delete(&model.SchemaMigration{}).Error; e != nil {
		t.Fatal(e)
	}
	for _, column := range []string{"Deleted", "NamespaceHash", "ReplayPending"} {
		if e = st.DB().Migrator().DropColumn(&model.BackgroundJob{}, column); e != nil {
			t.Fatal(e)
		}
	}
	var released []model.SchemaMigration
	if e = st.DB().Order("version").Find(&released).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		st, e = Open(config.DatabaseConfig{Path: path})
		if e != nil {
			t.Fatal(e)
		}
		var got model.BackgroundJob
		if e = st.DB().Where("id = ?", row.ID).First(&got).Error; e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(row, got) {
			t.Fatalf("v14 rewrote v13 accepted facts: before=%+v after=%+v", row, got)
		}
		var ledger []model.SchemaMigration
		if e = st.DB().Order("version").Find(&ledger).Error; e != nil || len(ledger) != len(released)+6 || ledger[len(ledger)-6].Version != 14 || ledger[len(ledger)-5].Version != 15 || ledger[len(ledger)-4].Version != 16 || ledger[len(ledger)-3].Version != 17 || ledger[len(ledger)-2].Version != 18 || ledger[len(ledger)-1].Version != 19 {
			t.Fatal("v14 migration not appended once", e)
		}
		for i, old := range released {
			if old.Version != ledger[i].Version || !old.AppliedAt.Equal(ledger[i].AppliedAt) {
				t.Fatal("v14 rewrote released migration evidence")
			}
		}
		if checkMigrationVersion(st.DB(), 13) == nil {
			t.Fatal("v13 binary accepted v14 schema")
		}
		if e = st.Close(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestBackgroundV15UpgradePreservesV14TaskEventAndMigrationFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14-background.db")
	st, e := Open(config.DatabaseConfig{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	row := model.BackgroundJob{ID: strings.Repeat("1", 64), PublicID: "resp_" + strings.Repeat("2", 64), UserID: 4, TokenID: 5, Operation: "responses", Model: "m", KeyHash: strings.Repeat("3", 64), NamespaceHash: strings.Repeat("4", 64), RequestHash: strings.Repeat("5", 64), DigestVersion: strings.Repeat("6", 64), Stage: "terminal", Stream: true, Revision: 17, ClaimEpoch: 8, LastSequence: 0, EventCount: 1, Ciphertext: []byte("v14-input"), Snapshot: []byte("v14-private-snapshot"), ContentExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	event := model.BackgroundEvent{TaskID: row.ID, Sequence: 0, Ciphertext: []byte("v14-event-ciphertext")}
	for _, item := range []interface{}{&row, &event} {
		if e = st.DB().Create(item).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = st.DB().Where("version >= 15").Delete(&model.SchemaMigration{}).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.DB().Migrator().DropColumn(&model.BackgroundJob{}, "ReplayPending"); e != nil {
		t.Fatal(e)
	}
	var released []model.SchemaMigration
	if e = st.DB().Order("version").Find(&released).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		st, e = Open(config.DatabaseConfig{Path: path})
		if e != nil {
			t.Fatal(e)
		}
		var got model.BackgroundJob
		var gotEvent model.BackgroundEvent
		if e = st.DB().First(&got, "id = ?", row.ID).Error; e != nil || !reflect.DeepEqual(got, row) {
			t.Fatal("v15 altered v14 task facts", got, e)
		}
		if e = st.DB().First(&gotEvent, "task_id = ?", row.ID).Error; e != nil || !reflect.DeepEqual(gotEvent, event) {
			t.Fatal("v15 altered event ciphertext", e)
		}
		var ledger []model.SchemaMigration
		if e = st.DB().Order("version").Find(&ledger).Error; e != nil || len(ledger) != len(released)+5 || ledger[len(ledger)-5].Version != 15 || ledger[len(ledger)-4].Version != 16 || ledger[len(ledger)-3].Version != 17 || ledger[len(ledger)-2].Version != 18 || ledger[len(ledger)-1].Version != 19 {
			t.Fatal("v15 not appended once", e)
		}
		for i, old := range released {
			if old.Version != ledger[i].Version || !old.AppliedAt.Equal(ledger[i].AppliedAt) {
				t.Fatal("released migration rewritten")
			}
		}
		if checkMigrationVersion(st.DB(), 14) == nil {
			t.Fatal("v14 binary accepted replay semantics it cannot understand")
		}
		if e = st.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
