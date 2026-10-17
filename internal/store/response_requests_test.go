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

func responseFixture(t *testing.T, st *Store, n int) model.ResponseRequest {
	t.Helper()
	user, err := st.CreateUser(&UserInput{Username: fmt.Sprintf("response-owner-%d", n), Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.CreateToken(&TokenInput{UserID: user.ID, Key: fmt.Sprintf("fixture-response-secret-%d", n), ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id := fmt.Sprintf("%064x", n)
	return model.ResponseRequest{ID: id, UserID: user.ID, TokenID: key.ID, Operation: "responses", KeyHash: strings.Repeat("b", 64), RequestHash: strings.Repeat("c", 64),
		DigestVersion: strings.Repeat("a", 64), ResponseID: "resp_" + id, Model: "m", Store: true, State: model.ResponseDispatching, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
}

func TestResponseRequestsConcurrentScopeAndCurrentAuthority(t *testing.T) {
	st := openTestStore(t, "responses-scope")
	defer st.Close()
	in := responseFixture(t, st, 1)
	ctx := context.Background()
	if err := st.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	limits := model.ResponseRequestLimits{InstanceRecords: 32, OwnerRecords: 16}
	var fresh atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			candidate := in
			candidate.ID = fmt.Sprintf("%064x", i+100)
			candidate.ResponseID = "resp_" + candidate.ID
			row, created, err := st.BeginResponseRequest(ctx, candidate, limits)
			if err != nil || row.RequestHash != in.RequestHash {
				t.Errorf("scope lookup: row=%+v err=%v", row, err)
			}
			if created {
				fresh.Add(1)
			}
		}(i)
	}
	workers.Wait()
	if fresh.Load() != 1 {
		t.Fatalf("fresh executors=%d", fresh.Load())
	}
	bad := in
	bad.RequestHash = strings.Repeat("d", 64)
	if _, _, err := st.BeginResponseRequest(ctx, bad, limits); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("different payload: %v", err)
	}
	if err := st.DB().Model(&model.Token{}).Where("id = ?", in.TokenID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginResponseRequest(ctx, in, limits); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("revoked replay accepted: %v", err)
	}
}

func TestResponseRequestsCompletionRecoveryAndCapacity(t *testing.T) {
	st := openTestStore(t, "responses-outcomes")
	defer st.Close()
	in := responseFixture(t, st, 2)
	ctx := context.Background()
	if err := st.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	limits := model.ResponseRequestLimits{InstanceRecords: 1, OwnerRecords: 1}
	row, fresh, err := st.BeginResponseRequest(ctx, in, limits)
	if err != nil || !fresh {
		t.Fatalf("admission: %+v %v", row, err)
	}
	complete := model.ResponseRequestCompletion{State: model.ResponseCompleted, NativeStatus: "completed", StatusCode: 200, HTTPStatus: 200, UpstreamResponseID: "resp-upstream"}
	tampered := row
	tampered.Model = "another-model"
	if err := st.FinishResponseRequest(ctx, tampered, complete); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("identity mutation accepted: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := st.FinishResponseRequest(ctx, row, complete); err != nil {
			t.Fatal(err)
		}
	}
	changed := complete
	changed.State = model.ResponseFailed
	if err := st.FinishResponseRequest(ctx, row, changed); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("completion overwritten: %v", err)
	}
	other := in
	other.ID, other.ResponseID, other.KeyHash = strings.Repeat("e", 64), "resp_"+strings.Repeat("e", 64), strings.Repeat("f", 64)
	if _, _, err := st.BeginResponseRequest(ctx, other, limits); !errors.Is(err, model.ErrResponseCapacity) {
		t.Fatalf("quota bypass: %v", err)
	}
	if n, err := st.CleanupResponseRequests(ctx, time.Now().Add(2*time.Hour), 1); err != nil || n != 1 {
		t.Fatalf("terminal cleanup: %d %v", n, err)
	}
	row, fresh, err = st.BeginResponseRequest(ctx, other, limits)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	if n, err := st.RecoverResponseRequests(ctx, 1); err != nil || n != 1 {
		t.Fatalf("startup recovery: %d %v", n, err)
	}
	if err := st.DB().Model(&model.ResponseRequest{}).Where("id = ?", row.ID).Update("expires_at", time.Now().Add(-time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := st.CleanupResponseRequests(ctx, time.Now().Add(24*time.Hour), 1); err != nil || n != 0 {
		t.Fatalf("unknown receipt erased: %d %v", n, err)
	}
	got, fresh, err := st.BeginResponseRequest(ctx, other, limits)
	if err != nil || fresh || got.State != model.ResponseUncertain {
		t.Fatalf("unknown receipt redispatched: %+v fresh=%v err=%v", got, fresh, err)
	}
	if err := st.FinishResponseRequest(ctx, row, complete); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("stale completion overwrote recovery: %v", err)
	}
}

func TestResponseRequestsRestartAndCrossStoreAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "responses-restart.db")
	first, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	in := responseFixture(t, first, 4)
	ctx := context.Background()
	if err := first.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	second, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	limits := model.ResponseRequestLimits{OwnerRecords: 1, InstanceRecords: 1}
	start := make(chan struct{})
	var workers sync.WaitGroup
	var created, capacity atomic.Int32
	for i, st := range []*Store{first, second} {
		workers.Add(1)
		go func(i int, st *Store) {
			defer workers.Done()
			<-start
			candidate := in
			candidate.ID = fmt.Sprintf("%064x", 400+i)
			candidate.ResponseID = "resp_" + candidate.ID
			candidate.KeyHash = fmt.Sprintf("%064x", 500+i)
			_, fresh, err := st.BeginResponseRequest(ctx, candidate, limits)
			if fresh && err == nil {
				created.Add(1)
			} else if errors.Is(err, model.ErrResponseCapacity) {
				capacity.Add(1)
			} else {
				t.Errorf("cross-store admission: fresh=%v err=%v", fresh, err)
			}
		}(i, st)
	}
	close(start)
	workers.Wait()
	if created.Load() != 1 || capacity.Load() != 1 {
		t.Fatalf("quota was not atomic across stores: created=%d capacity=%d", created.Load(), capacity.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []int64{1, 0} {
		if n, err := reopened.RecoverResponseRequests(ctx, 1); err != nil || n != expected {
			t.Fatalf("bounded restart recovery: got=%d expected=%d err=%v", n, expected, err)
		}
	}
	var retained model.ResponseRequest
	if err := reopened.DB().First(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if retained.State != model.ResponseUncertain || retained.OutputRecordID != "" {
		t.Fatalf("restart invented a known outcome: %+v", retained)
	}
	candidate := retained
	candidate.State = model.ResponseDispatching
	if _, fresh, err := reopened.BeginResponseRequest(ctx, candidate, limits); err != nil || fresh {
		t.Fatalf("restart redispatched execution: fresh=%v err=%v", fresh, err)
	}
}

func TestResponseRequestsScopeAndModelAuthorization(t *testing.T) {
	st := openTestStore(t, "responses-authority")
	defer st.Close()
	ctx := context.Background()
	first := responseFixture(t, st, 5)
	second := responseFixture(t, st, 6)
	if err := st.InitializeResponseRequests(ctx, first.DigestVersion); err != nil {
		t.Fatal(err)
	}
	limits := model.ResponseRequestLimits{InstanceRecords: 8, OwnerRecords: 4}
	for _, in := range []model.ResponseRequest{first, second} {
		if _, fresh, err := st.BeginResponseRequest(ctx, in, limits); err != nil || !fresh {
			t.Fatalf("same key hash must be isolated by owner/key: fresh=%v err=%v", fresh, err)
		}
	}
	forged := first
	forged.TokenID = second.TokenID
	if _, _, err := st.BeginResponseRequest(ctx, forged, limits); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("foreign token binding accepted: %v", err)
	}
	if err := st.SetSystemPolicy(access.Policy{Mode: access.Allowlist, Models: []string{"another-model"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginResponseRequest(ctx, first, limits); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("replay bypassed current system model policy: %v", err)
	}
}

func TestResponseRequestsCompletionRequiresOwnedCommittedArchive(t *testing.T) {
	st := openTestStore(t, "responses-archive-binding")
	defer st.Close()
	ctx := context.Background()
	in := responseFixture(t, st, 7)
	if err := st.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	row, _, err := st.BeginResponseRequest(ctx, in, model.ResponseRequestLimits{InstanceRecords: 2, OwnerRecords: 2})
	if err != nil {
		t.Fatal(err)
	}
	record := model.GenerationRecord{ID: strings.Repeat("d", 64), UserID: in.UserID + 1, TokenID: in.TokenID, State: "sealed", Committed: true, CreatedAt: in.CreatedAt, ExpiresAt: in.ExpiresAt}
	if err := st.DB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	completion := model.ResponseRequestCompletion{State: model.ResponseCompleted, NativeStatus: "completed", HTTPStatus: 200, OutputRecordID: record.ID}
	if err := st.FinishResponseRequest(ctx, row, completion); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("foreign archive accepted: %v", err)
	}
	if err := st.DB().Model(&record).Updates(map[string]any{"user_id": in.UserID, "committed": false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.FinishResponseRequest(ctx, row, completion); !errors.Is(err, model.ErrResponseConflict) {
		t.Fatalf("uncommitted archive accepted: %v", err)
	}
	if err := st.DB().Model(&record).Update("committed", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.FinishResponseRequest(ctx, row, completion); err != nil {
		t.Fatal(err)
	}
}

func TestResponseRequestsNamespaceCannotRotateOrDisappear(t *testing.T) {
	st := openTestStore(t, "responses-namespace")
	defer st.Close()
	in := responseFixture(t, st, 3)
	ctx := context.Background()
	if err := st.InitializeResponseRequests(ctx, in.DigestVersion); err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeResponseRequests(ctx, strings.Repeat("d", 64)); !errors.Is(err, model.ErrResponseUnavailable) {
		t.Fatalf("namespace rotated: %v", err)
	}
	if _, _, err := st.BeginResponseRequest(ctx, in, model.ResponseRequestLimits{InstanceRecords: 2, OwnerRecords: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Where("key = ?", responseDigestSetting).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeResponseRequests(ctx, in.DigestVersion); !errors.Is(err, model.ErrResponseUnavailable) {
		t.Fatalf("missing namespace silently recreated over receipts: %v", err)
	}
}
