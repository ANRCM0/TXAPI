package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundHistoryFixture(t *testing.T) (*Store, identity.Binding, bs.Record, model.ResponseHistory, bs.Limits) {
	t.Helper()
	s := openTestStore(t, "background-history")
	t.Cleanup(func() { s.Close() })
	owner, err := s.CreateUser(&UserInput{Username: "background-parent", Password: "background-password-123!", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.IssueToken(owner.ID, &TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	binding := identity.Binding{UserID: owner.ID, TokenID: key.ID}
	digest := strings.Repeat("a", 64)
	if _, err = s.InitializeBackground(context.Background(), digest, "disabled"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in := bs.Record{ID: strings.Repeat("b", 64), PublicID: "resp_" + strings.Repeat("c", 64), UserID: owner.ID, TokenID: key.ID, Operation: "responses", Model: "m", KeyHash: strings.Repeat("d", 64), NamespaceHash: strings.Repeat("e", 64), RequestHash: strings.Repeat("f", 64), DigestVersion: digest, Stage: string(bg.StageQueued), Revision: 1, CreatedAt: now.UnixMilli(), Deadline: now.Add(time.Minute).UnixMilli(), ContentExpiresAt: now.Add(time.Hour).UnixMilli(), ExpiresAt: now.Add(24 * time.Hour).UnixMilli(), ReservedBytes: 256, Ciphertext: []byte(strings.Repeat("x", 40)), LastSequence: -1}
	parent := model.ResponseHistory{ID: "resp_parent", UserID: owner.ID, TokenID: key.ID, Kind: "response", Model: "m", Version: 3, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), Ciphertext: []byte("opaque-parent")}
	if err = s.DB().Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	limits := bs.Limits{InstanceRecords: 4, OwnerRecords: 4, InstanceBytes: 4 << 20, OwnerBytes: 4 << 20, InstanceActive: 4, OwnerActive: 4}
	return s, binding, in, parent, limits
}

func TestBackgroundHistoryGuardRejectsChangedOrInaccessibleParent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.ResponseHistory)
		want   error
	}{
		{"version", func(p *model.ResponseHistory) { p.Version++ }, bg.ErrConflict},
		{"busy", func(p *model.ResponseHistory) { p.Busy = "other" }, bg.ErrConflict},
		{"deleted", func(p *model.ResponseHistory) { p.Deleted = true }, bg.ErrNotFound},
		{"expired", func(p *model.ResponseHistory) { p.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli() }, bg.ErrNotFound},
		{"owner", func(p *model.ResponseHistory) { p.UserID++ }, bg.ErrNotFound},
		{"key", func(p *model.ResponseHistory) { p.TokenID++ }, bg.ErrNotFound},
		{"model", func(p *model.ResponseHistory) { p.Model = "other" }, bg.ErrNotFound},
		{"kind", func(p *model.ResponseHistory) { p.Kind = "conversation" }, bg.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, in, parent, limits := backgroundHistoryFixture(t)
			guard := state.HistoryGuard{ID: parent.ID, Version: parent.Version}
			tc.mutate(&parent)
			if err := s.DB().Save(&parent).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := s.AdmitBackground(context.Background(), in, limits, guard); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			var count int64
			if err := s.DB().Model(&model.BackgroundJob{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("failed guard persisted a task: %d %v", count, err)
			}
		})
	}
}

func TestBackgroundHistoryGuardAdmissionLookupAndCurrentAuthority(t *testing.T) {
	s, binding, in, parent, limits := backgroundHistoryFixture(t)
	ctx := context.Background()
	guard := state.HistoryGuard{ID: parent.ID, Version: parent.Version}
	if _, found, err := s.LookupBackgroundSubmission(ctx, binding, in.KeyHash, in.RequestHash, in.Operation, in.Model); err != nil || found {
		t.Fatalf("initial lookup: %v %v", found, err)
	}
	got, err := s.AdmitBackground(ctx, in, limits, guard)
	if err != nil || got.ID != in.ID {
		t.Fatal(got, err)
	}
	var retained model.ResponseHistory
	if err = s.DB().First(&retained, "id = ?", parent.ID).Error; err != nil || retained.Version != parent.Version || retained.Busy != "" {
		t.Fatal("parent was mutated", retained, err)
	}
	if err = s.DB().Delete(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if got, err = s.AdmitBackground(ctx, in, limits, guard); err != nil || got.ID != in.ID {
		t.Fatal("idempotent hit required vanished parent", err)
	}
	if got, found, err := s.LookupBackgroundSubmission(ctx, binding, in.KeyHash, in.RequestHash, in.Operation, in.Model); err != nil || !found || got.ID != in.ID {
		t.Fatal("lookup hit", got, found, err)
	}
	if _, found, err := s.LookupBackgroundSubmission(ctx, binding, in.KeyHash, strings.Repeat("0", 64), in.Operation, in.Model); !found || !errors.Is(err, bg.ErrConflict) {
		t.Fatal("lookup conflict", found, err)
	}
	if err = s.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitBackground(ctx, in, limits, guard); !errors.Is(err, bg.ErrNotFound) {
		t.Fatal("revoked admission", err)
	}
	if _, _, err = s.LookupBackgroundSubmission(ctx, binding, in.KeyHash, in.RequestHash, in.Operation, in.Model); !errors.Is(err, bg.ErrNotFound) {
		t.Fatal("revoked lookup", err)
	}
}

func TestBackgroundSubmissionLookupTombstonesAndActiveRetention(t *testing.T) {
	for _, stage := range []string{string(bg.StageQueued), string(bg.StageTerminal), "deleted"} {
		t.Run(stage, func(t *testing.T) {
			s, binding, in, _, limits := backgroundHistoryFixture(t)
			ctx := context.Background()
			if _, err := s.AdmitBackground(ctx, in, limits); err != nil {
				t.Fatal(err)
			}
			updates := map[string]any{"content_expires_at": time.Now().Add(-time.Minute).UnixMilli()}
			if stage == "deleted" {
				updates["deleted"] = true
			} else {
				updates["stage"] = stage
			}
			if err := s.DB().Model(&model.BackgroundJob{}).Where("id = ?", in.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			_, found, err := s.LookupBackgroundSubmission(ctx, binding, in.KeyHash, in.RequestHash, in.Operation, in.Model)
			if !found || stage == string(bg.StageQueued) && err != nil || stage != string(bg.StageQueued) && !errors.Is(err, bg.ErrCursorUnavailable) {
				t.Fatal(found, err)
			}
		})
	}
}

func TestBackgroundHistoryAdmissionRollsBackAfterGuard(t *testing.T) {
	s, _, in, parent, limits := backgroundHistoryFixture(t)
	injected := errors.New("injected task insert failure")
	name := "background_history_insert_failure"
	if err := s.DB().Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "background_jobs" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB().Callback().Create().Remove(name) })
	if _, err := s.AdmitBackground(context.Background(), in, limits, state.HistoryGuard{ID: parent.ID, Version: parent.Version}); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var count int64
	if err := s.DB().Model(&model.BackgroundJob{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
	var retained model.ResponseHistory
	if err := s.DB().First(&retained, "id = ?", parent.ID).Error; err != nil || retained.Version != parent.Version || retained.Busy != "" {
		t.Fatal(retained, err)
	}
}
