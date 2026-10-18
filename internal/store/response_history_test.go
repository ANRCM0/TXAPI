package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

func TestResponseHistoryCASCapacityAndMultiRecordRollback(t *testing.T) {
	s := openTestStore(t, "history-transactions")
	defer s.Close()
	owner, err := s.CreateUser(&UserInput{Username: "history-owner", Password: "history-password-123!", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.IssueToken(owner.ID, &TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	b := access.Binding{UserID: owner.ID, TokenID: key.ID}
	if err := s.InitializeResponseRequests(context.Background(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	record := func(id string) model.ResponseHistory {
		return model.ResponseHistory{ID: id, UserID: b.UserID, TokenID: b.TokenID, Kind: "conversation", Model: "m", Version: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), Ciphertext: []byte("opaque-fixture"), ReservedBytes: 100}
	}
	limits := model.HistoryLimits{InstanceRecords: 4, OwnerRecords: 2, InstanceBytes: 4 << 20, OwnerBytes: 2 << 20}
	r := record("conv_original")
	if err := s.ApplyResponseHistory(context.Background(), b, []model.HistoryChange{{Record: r}}, nil, limits); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var won atomic.Int64
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := r
			next.Version = 2
			next.Ciphertext = []byte(fmt.Sprintf("revision-%d", i))
			if err := s.ApplyResponseHistory(context.Background(), b, []model.HistoryChange{{Expected: 1, Record: next}}, nil, limits); err == nil {
				won.Add(1)
			} else if !errors.Is(err, model.ErrHistoryConflict) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("CAS accepted %d writers", won.Load())
	}
	current, err := s.ReadResponseHistory(context.Background(), b, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := record("conv_second")
	if err := s.ApplyResponseHistory(context.Background(), b, []model.HistoryChange{{Record: second}}, nil, limits); err != nil {
		t.Fatal(err)
	}
	updated := current
	updated.Version++
	updated.Ciphertext = []byte("must-rollback")
	third := record("conv_third")
	err = s.ApplyResponseHistory(context.Background(), b, []model.HistoryChange{{Expected: current.Version, Record: updated}, {Record: third}}, nil, limits)
	if !errors.Is(err, model.ErrHistoryCapacity) {
		t.Fatal(err)
	}
	retained, err := s.ReadResponseHistory(context.Background(), b, r.ID)
	if err != nil || retained.Version != current.Version || string(retained.Ciphertext) == "must-rollback" {
		t.Fatal("capacity failure partially committed")
	}
	if _, err = s.ReadResponseHistory(context.Background(), access.Binding{UserID: b.UserID, TokenID: b.TokenID + 100}, r.ID); !errors.Is(err, model.ErrHistoryNotFound) {
		t.Fatalf("wrong key enumeration: %v", err)
	}
}

func TestResponseHistoryV12MigrationPreservesV11ReceiptsAndFinancialState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v11-history.db")
	s, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	receipt := model.ResponseRequest{ID: strings.Repeat("a", 64), ResponseID: "resp_preserved", UserID: 1, TokenID: 2, Operation: "responses", KeyHash: strings.Repeat("b", 64), RequestHash: strings.Repeat("c", 64), DigestVersion: strings.Repeat("d", 64), State: model.ResponseUncertain, Model: "m", CreatedAt: 1, ExpiresAt: 2}
	if err := s.DB().Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	balance := model.BillingBalance{OwnerType: "user", OwnerID: 1, ReservedUnits: 71, UsedUnits: 19}
	if err := s.DB().Create(&balance).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Where("version >= 12").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Migrator().DropTable(&model.ResponseHistory{}); err != nil {
		t.Fatal(err)
	}
	var released []model.SchemaMigration
	_ = s.DB().Order("version").Find(&released).Error
	_ = s.Close()
	for range 2 {
		s, err = Open(config.DatabaseConfig{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		var rows []model.SchemaMigration
		_ = s.DB().Order("version").Find(&rows).Error
		if len(rows) != len(released)+2 || rows[len(rows)-2].Version != 12 || rows[len(rows)-1].Version != 13 {
			t.Fatal("v12 not idempotent")
		}
		for i := range released {
			if rows[i].Version != released[i].Version || !rows[i].AppliedAt.Equal(released[i].AppliedAt) {
				t.Fatal("released migrations changed")
			}
		}
		var retained model.ResponseRequest
		if err := s.DB().First(&retained, "id = ?", receipt.ID).Error; err != nil || retained != receipt {
			t.Fatalf("receipt rewritten: %+v %v", retained, err)
		}
		var count int64
		if err := s.DB().Model(&model.ResponseHistory{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("history inferred from receipts")
		}
		var retainedBalance model.BillingBalance
		if err := s.DB().First(&retainedBalance).Error; err != nil || retainedBalance.UsedUnits != 19 || retainedBalance.ReservedUnits != 71 {
			t.Fatal("financial state changed")
		}
		if checkMigrationVersion(s.DB(), 11) == nil {
			t.Fatal("old binary admitted schema v12")
		}
		_ = s.Close()
	}
}
