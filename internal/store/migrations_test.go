package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestResponseV11UpgradePreservesV10FinancialAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v10-responses-upgrade.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version >= 11").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Migrator().DropTable(&model.ResponseRequest{}); err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	if err := st.DB().Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	if len(released) == 0 || released[len(released)-1].Version != 10 {
		t.Fatal("fixture is not schema v10")
	}
	hold := model.BillingReservation{ID: "v10-original-hold", UserID: 7, TokenID: 8, State: "pending", ReservedUnits: 71, ProviderHeldUnits: 23, PriceVersion: "v10-original-price", LeaseUntil: time.Now().Add(time.Minute).UnixMilli()}
	if err := st.DB().Create(&hold).Error; err != nil {
		t.Fatal(err)
	}
	balance := model.BillingBalance{OwnerType: "user", OwnerID: 7, ReservedUnits: 71, UsedUnits: 9}
	if err := st.DB().Create(&balance).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var got []model.SchemaMigration
		if err := st.DB().Order("version").Find(&got).Error; err != nil {
			t.Fatal(err)
		}
		if len(got) != len(released)+2 || got[len(got)-2].Version != 11 || got[len(got)-1].Version != 12 {
			t.Fatalf("v11 ledger is not idempotent: %+v", got)
		}
		for i := range released {
			if got[i].Version != released[i].Version || !got[i].AppliedAt.Equal(released[i].AppliedAt) {
				t.Fatal("v11 altered released ledger")
			}
		}
		var retained model.BillingReservation
		if err := st.DB().First(&retained, "id = ?", hold.ID).Error; err != nil || retained.State != hold.State || retained.ReservedUnits != hold.ReservedUnits || retained.ProviderHeldUnits != hold.ProviderHeldUnits || retained.PriceVersion != hold.PriceVersion || retained.LeaseUntil != hold.LeaseUntil {
			t.Fatalf("v11 changed original financial hold: %+v %v", retained, err)
		}
		var retainedBalance model.BillingBalance
		if err := st.DB().First(&retainedBalance, "owner_type = ? AND owner_id = ?", balance.OwnerType, balance.OwnerID).Error; err != nil || retainedBalance.ReservedUnits != balance.ReservedUnits || retainedBalance.UsedUnits != balance.UsedUnits {
			t.Fatalf("v11 changed original financial balance: %+v %v", retainedBalance, err)
		}
		var receipts int64
		if err := st.DB().Model(&model.ResponseRequest{}).Count(&receipts).Error; err != nil || receipts != 0 {
			t.Fatalf("v11 invented historical response authority: %d %v", receipts, err)
		}
		if err := checkMigrationVersion(st.DB(), 10); err == nil {
			t.Fatal("v10 binary accepted schema v11")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntentV10UpgradePreservesV9IntentAndOriginalFinancialAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9-claim-upgrade.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	channel := encryptedChannel(t, st, "v9", "legacy-v9-fixture-key")
	id := strings.Repeat("a", 64)
	original := model.GenerationIntent{ID: id, UserID: 7, TokenID: 8, AdmissionKey: strings.Repeat("b", 64), RequestDigest: strings.Repeat("c", 64), DigestVersion: strings.Repeat("d", 64), Operation: "chat", Model: "m", BillingID: id[:32], State: model.IntentQueued, Ciphertext: bytes.Repeat([]byte{42}, 64), CreatedAt: time.Now().UnixMilli(), Deadline: time.Now().Add(time.Minute).UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := st.DB().Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingReservation{ID: original.BillingID, UserID: 7, TokenID: 8, OriginModel: "m", PriceVersion: "v9-price", ReservedUnits: 71, State: "reserved", LeaseUntil: original.Deadline}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: 7, ReservedUnits: 71, UsedUnits: 9}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version = 10").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_intent_output", "idx_credential_key_hash", "idx_generation_intents_claim_until"} {
		if err := st.DB().Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ProviderPolicyVersion", "ClaimEpoch", "ClaimHash", "ClaimUntil", "CancelRequested", "OutputRecordID"} {
		if err := st.DB().Migrator().DropColumn(&model.GenerationIntent{}, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"KeyCiphertext", "KeyHash"} {
		if err := st.DB().Migrator().DropColumn(&model.Credential{}, name); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var got model.GenerationIntent
		if err := st.DB().First(&got, "id = ?", original.ID).Error; err != nil || !bytes.Equal(got.Ciphertext, original.Ciphertext) || got.ClaimEpoch != 0 || got.ProviderPolicyVersion != "" || got.OutputRecordID != "" || got.State != model.IntentQueued {
			t.Fatal("v10 invented execution authority or changed v9 content", err)
		}
		var financial model.BillingReservation
		if err := st.DB().First(&financial, "id = ?", original.BillingID).Error; err != nil || financial.ReservedUnits != 71 || financial.State != "reserved" || financial.PriceVersion != "v9-price" {
			t.Fatal("v10 changed original hold", err)
		}
		var balance model.BillingBalance
		if err := st.DB().First(&balance).Error; err != nil || balance.ReservedUnits != 71 || balance.UsedUnits != 9 {
			t.Fatal("v10 changed financial balance", err)
		}
		cred, err := st.GetCredential(channel.Credentials[0].ID)
		if err != nil || cred.APIKey != "legacy-v9-fixture-key" {
			t.Fatal("compatibility upgrade altered credential", err)
		}
		if err := checkMigrationVersion(st.DB(), 9); err == nil {
			t.Fatal("v9 binary accepted v10 database")
		}
		var count int64
		if err := st.DB().Model(&model.SchemaMigration{}).Where("version = 10").Count(&count).Error; err != nil || count != 1 {
			t.Fatal("v10 was not idempotent", err)
		}
		_ = st.Close()
	}
}

func TestV10IndexFailureRollsBackBothIndexesAndVersion(t *testing.T) {
	st := openTestStore(t, "v10-index-failure")
	defer st.Close()
	channel := encryptedChannel(t, st, "bad-duplicate-fixture", "first")
	if _, err := st.CreateCredential(channel.ID, &CredentialInput{APIKey: "second"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_intent_output", "idx_credential_key_hash"} {
		if err := st.DB().Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DB().Model(&model.Credential{}).Where("channel_id = ?", channel.ID).UpdateColumn("key_hash", "duplicate-corrupt-fixture").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version = 10").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyDataMigrations(st.DB(), dataMigrations); err == nil {
		t.Fatal("conflicting unique index accepted")
	}
	if st.DB().Migrator().HasIndex(&model.GenerationIntent{}, "idx_intent_output") {
		t.Fatal("first DDL escaped failed migration transaction")
	}
	var count int64
	if err := st.DB().Model(&model.SchemaMigration{}).Where("version = 10").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed v10 recorded completion", err)
	}
}

func TestArchiveV8UpgradePreservesReleasedLedgerAndRejectsV7(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7-archive-upgrade.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version >= 8").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Migrator().DropTable(&model.GenerationIntent{}, &model.GenerationSegment{}, &model.GenerationRecord{}); err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	if err := st.DB().Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("archive-upgrade-preserve", "existing-value"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var got []model.SchemaMigration
		if err := st.DB().Order("version").Find(&got).Error; err != nil {
			t.Fatal(err)
		}
		if len(got) != len(released)+5 || got[len(got)-5].Version != 8 || got[len(got)-4].Version != 9 || got[len(got)-3].Version != 10 || got[len(got)-2].Version != 11 || got[len(got)-1].Version != 12 {
			t.Fatalf("non-idempotent v8/v9/v10/v11: %+v", got)
		}
		for i := range released {
			if got[i].Version != released[i].Version || !got[i].AppliedAt.Equal(released[i].AppliedAt) {
				t.Fatal("released ledger changed")
			}
		}
		if value, err := st.GetSetting("archive-upgrade-preserve"); err != nil || value != "existing-value" {
			t.Fatalf("pre-existing state changed: %s %v", value, err)
		}
		if err := checkMigrationVersion(st.DB(), 7); err == nil {
			t.Fatal("v7 accepted v8 database")
		}
		if !st.DB().Migrator().HasTable(&model.GenerationSegment{}) {
			t.Fatal("archive schema missing")
		}
		if n, err := st.InterruptGenerationRecords(context.Background(), 1); err != nil || n != 0 {
			t.Fatalf("invented historical output: %d %v", n, err)
		}
		st.Close()
	}
}

func TestIntentV9UpgradePreservesV8OutputAndFinancialAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v8-intent-upgrade.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version >= 9").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Migrator().DropTable(&model.GenerationIntent{}); err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	if err := st.DB().Order("version").Find(&released).Error; err != nil {
		t.Fatal(err)
	}
	record := model.GenerationRecord{ID: "v8-retained-output", UserID: 1, TokenID: 2, State: "sealed", MaxBytes: 16384, StoredBytes: 40, NextSequence: 1,
		CreatedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Segments: []model.GenerationSegment{{RecordID: "v8-retained-output", Sequence: 0, Kind: "result", Ciphertext: []byte("opaque-existing-encrypted-observation")}}}
	if err := st.DB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	reservation := model.BillingReservation{ID: "existing-pending", UserID: 1, TokenID: 2, State: "pending", ReservedUnits: 71, ProviderHeldUnits: 23, PriceVersion: "existing-price-version", LeaseUntil: time.Now().Add(time.Minute).UnixMilli()}
	if err := st.DB().Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: 1, ReservedUnits: 71, UsedUnits: 9}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var got []model.SchemaMigration
		if err := st.DB().Order("version").Find(&got).Error; err != nil {
			t.Fatal(err)
		}
		if len(got) != len(released)+4 || got[len(got)-4].Version != 9 || got[len(got)-3].Version != 10 || got[len(got)-2].Version != 11 || got[len(got)-1].Version != 12 {
			t.Fatalf("non-idempotent v9/v10/v11 migration: %+v", got)
		}
		for i := range released {
			if got[i].Version != released[i].Version || !got[i].AppliedAt.Equal(released[i].AppliedAt) {
				t.Fatal("released version ledger changed")
			}
		}
		var old model.GenerationRecord
		if err := st.DB().Preload("Segments").First(&old, "id = ?", record.ID).Error; err != nil || len(old.Segments) != 1 || string(old.Segments[0].Ciphertext) != string(record.Segments[0].Ciphertext) {
			t.Fatalf("v8 content changed: %+v %v", old, err)
		}
		var bill model.BillingReservation
		if err := st.DB().First(&bill, "id = ?", reservation.ID).Error; err != nil || bill.State != "pending" || bill.ReservedUnits != 71 || bill.ProviderHeldUnits != 23 {
			t.Fatalf("financial authority changed: %+v %v", bill, err)
		}
		var balance model.BillingBalance
		if err := st.DB().First(&balance).Error; err != nil || balance.ReservedUnits != 71 || balance.UsedUnits != 9 {
			t.Fatalf("balance changed: %+v %v", balance, err)
		}
		var intents int64
		if err := st.DB().Model(&model.GenerationIntent{}).Count(&intents).Error; err != nil || intents != 0 {
			t.Fatalf("invented historical task: %d %v", intents, err)
		}
		if err := checkMigrationVersion(st.DB(), 8); err == nil {
			t.Fatal("v8 binary accepted v9")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

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
