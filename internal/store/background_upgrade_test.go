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
	if e = st.DB().Where("version = 14").Delete(&model.SchemaMigration{}).Error; e != nil {
		t.Fatal(e)
	}
	for _, column := range []string{"Deleted", "NamespaceHash"} {
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
		if e = st.DB().Order("version").Find(&ledger).Error; e != nil || len(ledger) != len(released)+1 || ledger[len(ledger)-1].Version != 14 {
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
