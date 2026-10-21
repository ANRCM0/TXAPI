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

func TestBackgroundBillingV22UpgradePreservesDisabledTasksAndFinancialAuthorities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-v21.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	task := model.BackgroundJob{ID: strings.Repeat("a", 64), PublicID: "resp_" + strings.Repeat("b", 64), UserID: 1, TokenID: 2, Operation: "responses", Model: "m", KeyHash: strings.Repeat("c", 64), RequestHash: strings.Repeat("d", 64), DigestVersion: strings.Repeat("e", 64), Stage: "remote_active", Revision: 4, ClaimEpoch: 2, LastSequence: 3, StoredBytes: 19, EventCount: 4, Ciphertext: []byte("opaque-v21-input"), Snapshot: []byte("opaque-v21-target"), CreatedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	reservation := model.BillingReservation{ID: "protected-hold", UserID: 1, TokenID: 2, OriginModel: "m", State: "pending", ReservedUnits: 53, ProviderBudgetUnits: 71, ProviderHeldUnits: 29, PriceVersion: "old-price"}
	balance := model.BillingBalance{OwnerType: "user", OwnerID: 1, ReservedUnits: 53, UsedUnits: 11}
	for _, row := range []any{&task, &reservation, &balance} {
		if err := st.DB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DB().First(&reservation, "id = ?", reservation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec("DROP INDEX idx_background_billing_id_unique").Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"billing_id", "billing_mode", "control_calls"} {
		if err := st.DB().Migrator().DropColumn(&model.BackgroundJob{}, column); err != nil {
			t.Fatal(column, err)
		}
	}
	if err := st.DB().Migrator().DropColumn(&model.BillingReservation{}, "provider_policy_version"); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("version = 22").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	if err := st.DB().Order("version").Find(&released).Error; err != nil {
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
		opened := st
		t.Cleanup(func() { _ = opened.Close() })
		var gotTask model.BackgroundJob
		var gotHold model.BillingReservation
		var gotBalance model.BillingBalance
		if err := st.DB().First(&gotTask).Error; err != nil || !reflect.DeepEqual(task, gotTask) {
			t.Fatalf("task retroactively charged or rewritten: %+v %v", gotTask, err)
		}
		if err := st.DB().First(&gotHold).Error; err != nil || !reflect.DeepEqual(reservation, gotHold) {
			t.Fatalf("hold rewritten: %+v %v", gotHold, err)
		}
		if err := st.DB().First(&gotBalance).Error; err != nil || !reflect.DeepEqual(balance, gotBalance) {
			t.Fatalf("balance rewritten: %+v %v", gotBalance, err)
		}
		var steps []model.SchemaMigration
		if err := st.DB().Order("version").Find(&steps).Error; err != nil || len(steps) != len(released)+1 || steps[len(steps)-1].Version != 22 || !reflect.DeepEqual(steps[:len(released)], released) {
			t.Fatalf("migration ledger changed: %+v %v", steps, err)
		}
		if checkMigrationVersion(st.DB(), 21) == nil {
			t.Fatal("v21 accepted v22 data")
		}
		if !st.DB().Migrator().HasIndex(&model.BackgroundJob{}, "idx_background_billing_id_unique") {
			t.Fatal("one task per financial reservation constraint missing")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
