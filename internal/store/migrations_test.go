package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestMigrationFailureRollsBackAndCanBeRetried(t *testing.T) {
	st := openTestStore(t, "migration-rollback")
	defer st.Close()
	attempted := 0
	version := latestMigrationVersion + 1
	step := dataMigration{Version: version, Apply: func(tx *gorm.DB) error {
		attempted++
		if err := tx.Create(&model.Setting{Key: "test-transaction-marker", Value: "test"}).Error; err != nil {
			return err
		}
		if attempted == 1 {
			return errors.New("controlled migration failure")
		}
		return nil
	}}
	if err := applyDataMigrations(st.DB(), []dataMigration{step}); err == nil {
		t.Fatal("failed migration accepted")
	}
	var ledger, changes int64
	st.DB().Model(&model.SchemaMigration{}).Where("version=?", version).Count(&ledger)
	st.DB().Model(&model.Setting{}).Where("key=?", "test-transaction-marker").Count(&changes)
	if ledger != 0 || changes != 0 {
		t.Fatalf("partial migration: ledger=%d changes=%d", ledger, changes)
	}
	if err := applyDataMigrations(st.DB(), []dataMigration{step}); err != nil {
		t.Fatal(err)
	}
	if err := applyDataMigrations(st.DB(), []dataMigration{step}); err != nil {
		t.Fatal(err)
	}
	if attempted != 2 {
		t.Fatalf("migration not idempotent: attempts=%d", attempted)
	}
}
func TestMigrationRejectsDuplicateVersionsBeforeApplying(t *testing.T) {
	st := openTestStore(t, "migration-registry")
	defer st.Close()
	calls := 0
	step := dataMigration{Version: latestMigrationVersion + 1, Apply: func(*gorm.DB) error { calls++; return nil }}
	if err := applyDataMigrations(st.DB(), []dataMigration{step, step}); err == nil || calls != 0 {
		t.Fatalf("invalid registry executed: err=%v calls=%d", err, calls)
	}
}
func TestOpenRejectsNewerSchemaBeforeChangingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.SchemaMigration{Version: 99, AppliedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	// If AutoMigrate runs before version verification, it recreates this table.
	if err := st.DB().Migrator().DropTable(&model.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := st.AutoMigrate(); err == nil {
		t.Fatal("accepted unsupported schema")
	}
	if st.DB().Migrator().HasTable(&model.Session{}) {
		t.Fatal("changed future schema before compatibility check")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(config.DatabaseConfig{Path: path}); err == nil {
		t.Fatal("older binary opened future schema")
	}
}

func TestS3V2CredentialUpgradePreservesDataAndRejectsDowngrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(&ChannelInput{Name: "upgrade", BaseURL: "http://upstream.test", Models: []string{"m"}, APIKey: "keep-secret"})
	if err != nil {
		t.Fatal(err)
	}
	id := ch.Credentials[0].ID
	if err = st.DB().Model(&model.Credential{}).Where("id=?", id).Update("fail_count", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Where("version>=3").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Migrator().DropColumn(&model.Credential{}, "HealthGeneration"); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Migrator().DropColumn(&model.Credential{}, "HealthObservedAt"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.GetCredential(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.APIKey != "keep-secret" || current.FailCount != 2 || current.HealthGeneration != 0 {
		t.Fatalf("upgrade corrupted data: %+v", current)
	}
	if err = checkMigrationVersion(st.DB(), 2); err == nil {
		t.Fatal("S2 binary should reject S3 database")
	}
	st.Close()
	st, err = Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var count int64
	st.DB().Model(&model.SchemaMigration{}).Where("version=3").Count(&count)
	if count != 1 {
		t.Fatalf("migration not idempotent: count=%d", count)
	}
}

func TestReviewV3ToV4ObservationUpgradeAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(&ChannelInput{Name: "v3", BaseURL: "http://upstream.test", Models: []string{"m"}, APIKey: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	id := ch.Credentials[0].ID
	if _, err = st.SetCredentialStatus(id, model.CredentialActive); err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Where("version=4").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Migrator().DropColumn(&model.Credential{}, "HealthObservedAt"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.GetCredential(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.APIKey != "keep" || got.HealthGeneration != 1 || got.HealthObservedAt != 0 {
			t.Fatalf("v4 changed existing identity/state: %+v", got)
		}
		if err = checkMigrationVersion(st.DB(), 3); err == nil {
			t.Fatal("v3 binary accepted v4 database")
		}
		var count int64
		st.DB().Model(&model.SchemaMigration{}).Where("version=4").Count(&count)
		if count != 1 {
			t.Fatal("v4 ledger not idempotent")
		}
		st.Close()
	}
}
