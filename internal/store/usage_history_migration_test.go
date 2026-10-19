package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestUsageHistoryV16UpgradePreservesV15AuthoritiesAndOpaqueContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-v15.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []any{
		&model.BillingPriceVersion{Version: strings.Repeat("f", 64), Payload: `{"format":1,"old":"immutable"}`},
		&model.BillingReservation{ID: "old-reservation", UserID: 7, TokenID: 8, PriceVersion: strings.Repeat("f", 64), State: "pending", ReservedUnits: 71, ProviderHeldUnits: 23, LeaseUntil: now.Add(time.Hour).UnixMilli()},
		&model.BillingBalance{OwnerType: "user", OwnerID: 7, UsedUnits: 19, ReservedUnits: 71},
		&model.BillingBalance{OwnerType: "token", OwnerID: 8, UsedUnits: 19, ReservedUnits: 71},
		&model.BillingAttempt{ReservationID: "old-reservation", Number: 1, State: "started", ActualModel: "m", Group: "g", ProviderBoundUnits: 23},
		&model.GenerationRecord{ID: "opaque-record", UserID: 7, TokenID: 8, State: "sealed", MaxBytes: 16384, StoredBytes: 31, NextSequence: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()},
		&model.GenerationSegment{RecordID: "opaque-record", Sequence: 0, Kind: "result", Ciphertext: []byte("private-authenticated-ciphertext")},
		&model.ResponseRequest{ID: "old-idempotency", UserID: 7, TokenID: 8, KeyHash: strings.Repeat("a", 64), ResponseID: "resp_prior", Operation: "responses", State: model.ResponseUncertain},
		&model.RequestLog{RequestID: "old-droppable-log", UserID: 7, TokenID: 8, PromptTokens: 17},
	}
	for _, row := range rows {
		if e := st.DB().Create(row).Error; e != nil {
			t.Fatal(e)
		}
	}
	tables := []string{"billing_price_versions", "billing_reservations", "billing_balances", "billing_attempts", "generation_records", "generation_segments", "response_requests", "logs"}
	snapshot := func(table string) []byte {
		t.Helper()
		var values []map[string]any
		if e := st.DB().Table(table).Find(&values).Error; e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(values)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	before := map[string][]byte{}
	for _, table := range tables {
		before[table] = snapshot(table)
	}
	if e := st.DB().Migrator().DropTable(&model.ManagedUpstreamAccount{}, &model.UsageAttemptReceipt{}, &model.UsageInvocation{}); e != nil {
		t.Fatal(e)
	}
	if e := st.DB().Where("version > ?", 15).Delete(&model.SchemaMigration{}).Error; e != nil {
		t.Fatal(e)
	}
	var released []model.SchemaMigration
	if e := st.DB().Order("version").Find(&released).Error; e != nil {
		t.Fatal(e)
	}
	if e := st.Close(); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		st, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			if !bytes.Equal(before[table], snapshot(table)) {
				t.Fatal("v16 changed old authority/content", table)
			}
		}
		var got []model.SchemaMigration
		if e := st.DB().Order("version").Find(&got).Error; e != nil || len(got) != len(released)+2 || got[len(got)-2].Version != 16 || got[len(got)-1].Version != 17 {
			t.Fatal(got, e)
		}
		for i, old := range released {
			if old.Version != got[i].Version || !old.AppliedAt.Equal(got[i].AppliedAt) {
				t.Fatal("rewrote released migration", old, got[i])
			}
		}
		for _, row := range []any{&model.UsageInvocation{}, &model.UsageAttemptReceipt{}} {
			var count int64
			if e := st.DB().Model(row).Count(&count).Error; e != nil || count != 0 {
				t.Fatal("backfilled invented usage", count, e)
			}
		}
		if e := checkMigrationVersion(st.DB(), 15); e == nil {
			t.Fatal("old binary accepted schema16")
		}
		if e := st.Close(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestUsageHistoryCleanupHardBatchBoundAndAtomicChildDeletion(t *testing.T) {
	st, e := Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "usage-retention.db")})
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	rows := make([]model.UsageInvocation, 1002)
	children := make([]model.UsageAttemptReceipt, 1002)
	for i := range rows {
		id := fmt.Sprintf("usage_%032x", i)
		rows[i] = model.UsageInvocation{ID: id, UserID: 1, TokenID: 2, State: "sealed", MaxAttempts: 1, AttemptCount: 1, ExpiresAt: 1}
		children[i] = model.UsageAttemptReceipt{InvocationID: id, Number: 1, State: "observed"}
	}
	if e = st.DB().CreateInBatches(&rows, 50).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.DB().CreateInBatches(&children, 50).Error; e != nil {
		t.Fatal(e)
	}
	if e = st.DB().Exec("CREATE TRIGGER fail_usage_delete BEFORE DELETE ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private cleanup failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if n, e := st.CleanupUsageHistory(context.Background(), 1000); e == nil || n != 0 {
		t.Fatal("cleanup did not fail atomically", n, e)
	}
	var count int64
	if e = st.DB().Model(&model.UsageAttemptReceipt{}).Count(&count).Error; e != nil || count != 1002 {
		t.Fatal("cleanup dropped children without parent", count, e)
	}
	if e = st.DB().Exec("DROP TRIGGER fail_usage_delete").Error; e != nil {
		t.Fatal(e)
	}
	if n, e := st.CleanupUsageHistory(context.Background(), 1000); e != nil || n != 1000 {
		t.Fatal(n, e)
	}
	if e = st.DB().Model(&model.UsageInvocation{}).Count(&count).Error; e != nil || count != 2 {
		t.Fatal(count, e)
	}
	if e = st.DB().Model(&model.UsageAttemptReceipt{}).Count(&count).Error; e != nil || count != 2 {
		t.Fatal(count, e)
	}
}
