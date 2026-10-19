package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"minigateway/internal/config"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"minigateway/internal/model"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type taskFixture struct {
	st                                  *Store
	owner, other, admin                 model.User
	session, otherSession, adminSession string
}

func taskTestFixture(t *testing.T) taskFixture {
	t.Helper()
	s, e := Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "tasks.db")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	f := taskFixture{st: s}
	for i, name := range []string{"task-owner", "task-other", "task-admin"} {
		role := "user"
		if i == 2 {
			role = "admin"
		}
		u, e := s.CreateUser(&UserInput{Username: name, Password: "task-password-123", Role: role, ModelMode: "all"})
		if e != nil {
			t.Fatal(e)
		}
		session, _, e := s.Login(name, "task-password-123")
		if e != nil {
			t.Fatal(e)
		}
		switch i {
		case 0:
			f.owner = *u
			f.session = session
		case 1:
			f.other = *u
			f.otherSession = session
		case 2:
			f.admin = *u
			f.adminSession = session
		}
	}
	if e = s.SetSetting(backgroundDigestSetting, strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f taskFixture) row(t *testing.T, n int, owner uint, stage string) model.BackgroundJob {
	t.Helper()
	now := time.Now()
	r := model.BackgroundJob{ID: fmt.Sprintf("%064x", n), PublicID: fmt.Sprintf("resp_%064x", n), UserID: owner, TokenID: uint(n + 10), Operation: "responses", Model: "m", KeyHash: fmt.Sprintf("%064x", n), Stage: stage, Revision: 1, CreatedAt: now.UnixMilli(), Deadline: now.Add(time.Hour).UnixMilli(), ContentExpiresAt: now.Add(time.Hour).UnixMilli(), ExpiresAt: now.Add(2 * time.Hour).UnixMilli(), Ciphertext: []byte("private-input-sentinel"), Snapshot: []byte("private-provider-handle-sentinel"), LastSequence: -1}
	if e := f.st.DB().Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	return r
}
func TestTaskControlScopePaginationAndMetadata(t *testing.T) {
	f := taskTestFixture(t)
	repo := f.st.TaskControl()
	ctx := context.Background()
	own := task.Access{Session: f.session}
	admin := task.Access{Session: f.adminSession, Administrator: true}
	a := f.row(t, 1, f.owner.ID, "queued")
	f.row(t, 2, f.owner.ID, "uncertain")
	foreign := f.row(t, 3, f.other.ID, "remote_active")
	page, e := repo.List(ctx, own, task.Query{Limit: 1})
	if e != nil || len(page.Data) != 1 || !page.HasMore || page.Data[0].UserID != f.owner.ID {
		t.Fatalf("page: %+v %v", page, e)
	}
	second, e := repo.List(ctx, own, task.Query{Limit: 1, Offset: 1})
	if e != nil || len(second.Data) != 1 || second.HasMore || second.Data[0].ID == page.Data[0].ID {
		t.Fatal(second, e)
	}
	if _, e = repo.Read(ctx, own, foreign.PublicID); !errors.Is(e, task.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = repo.List(ctx, own, task.Query{OwnerID: f.other.ID}); !errors.Is(e, task.ErrInvalid) {
		t.Fatal(e)
	}
	all, e := repo.List(ctx, admin, task.Query{})
	if e != nil || len(all.Data) != 3 {
		t.Fatal(all, e)
	}
	for _, r := range all.Data {
		if r.CanCancel {
			t.Fatal("admin gained cancellation")
		}
	}
	data, _ := json.Marshal(all)
	for _, secret := range []string{"private-input", "provider-handle", "ciphertext", "claim_hash"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private metadata leak")
		}
	}
	filtered, e := repo.List(ctx, admin, task.Query{OwnerID: f.owner.ID, TokenID: a.TokenID, Stage: "queued"})
	if e != nil || len(filtered.Data) != 1 || filtered.Data[0].LastSequence != nil {
		t.Fatal(filtered, e)
	}
}
func TestTaskControlRevalidatesSessionAndRole(t *testing.T) {
	f := taskTestFixture(t)
	repo := f.st.TaskControl()
	ctx := context.Background()
	r := f.row(t, 1, f.owner.ID, "remote_active")
	if _, e := repo.Read(ctx, task.Access{Session: "sk-not-a-session"}, r.PublicID); !errors.Is(e, task.ErrUnauthorized) {
		t.Fatal(e)
	}
	if _, e := repo.List(ctx, task.Access{Session: f.session, Administrator: true}, task.Query{}); !errors.Is(e, task.ErrForbidden) {
		t.Fatal(e)
	}
	if e := f.st.DB().Model(&model.User{}).Where("id = ?", f.admin.ID).Update("role", "user").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := repo.List(ctx, task.Access{Session: f.adminSession, Administrator: true}, task.Query{}); !errors.Is(e, task.ErrForbidden) {
		t.Fatal("demotion not applied", e)
	}
	if e := f.st.Logout(f.session); e != nil {
		t.Fatal(e)
	}
	if _, e := repo.Cancel(ctx, task.Access{Session: f.session}, r.PublicID, 1); !errors.Is(e, task.ErrUnauthorized) {
		t.Fatal("logout did not fence mutation", e)
	}
	if e := f.st.DB().Model(&model.User{}).Where("id = ?", f.other.ID).Update("status", "disabled").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := repo.List(ctx, task.Access{Session: f.otherSession}, task.Query{}); !errors.Is(e, task.ErrUnauthorized) {
		t.Fatal(e)
	}
}

func TestTaskControlRejectsSoftDeletedSessionOwner(t *testing.T) {
	f := taskTestFixture(t)
	repo := f.st.TaskControl()
	ctx := context.Background()
	r := f.row(t, 1, f.owner.ID, "remote_active")
	for _, user := range []model.User{f.owner, f.admin} {
		if err := f.st.DB().Delete(&user).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Keep the sessions and active status to test repository authorization itself.
	for _, a := range []task.Access{{Session: f.session}, {Session: f.adminSession, Administrator: true}} {
		if page, err := repo.List(ctx, a, task.Query{}); !errors.Is(err, task.ErrUnauthorized) || len(page.Data) != 0 {
			t.Fatalf("deleted user listed tasks: %+v %v", page, err)
		}
		if got, err := repo.Read(ctx, a, r.PublicID); !errors.Is(err, task.ErrUnauthorized) || got.ID != "" {
			t.Fatalf("deleted user read task: %+v %v", got, err)
		}
	}
	if got, err := repo.Cancel(ctx, task.Access{Session: f.session}, r.PublicID, r.Revision); !errors.Is(err, task.ErrUnauthorized) || got.ID != "" {
		t.Fatalf("deleted user cancelled task: %+v %v", got, err)
	}
	var stored model.BackgroundJob
	if err := f.st.DB().First(&stored, "id = ?", r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Revision != r.Revision || stored.CancelRequested || stored.Stage != r.Stage {
		t.Fatalf("unauthorized cancellation changed task: %+v", stored)
	}
}

func TestTaskControlCancellationCASAndUnknownProtection(t *testing.T) {
	f := taskTestFixture(t)
	repo := f.st.TaskControl()
	ctx := context.Background()
	own := task.Access{Session: f.session}
	queued := f.row(t, 1, f.owner.ID, "queued")
	unknown := f.row(t, 2, f.owner.ID, "uncertain")
	terminal := f.row(t, 3, f.owner.ID, "terminal")
	got, e := repo.Cancel(ctx, own, queued.PublicID, 1)
	if e != nil || got.Stage != "terminal" || !got.CancelRequested || got.Revision != 2 {
		t.Fatal(got, e)
	}
	if _, e = repo.Cancel(ctx, own, queued.PublicID, 1); !errors.Is(e, task.ErrConflict) {
		t.Fatal("stale revision accepted", e)
	}
	repeat, e := repo.Cancel(ctx, own, queued.PublicID, 2)
	if e != nil || repeat.Revision != 2 {
		t.Fatal(repeat, e)
	}
	got, e = repo.Cancel(ctx, own, unknown.PublicID, 1)
	if e != nil || got.Stage != "uncertain" || !got.CancelRequested {
		t.Fatal("unknown generation replayed or terminalized", got, e)
	}
	if e = f.st.DB().First(&unknown, "id = ?", unknown.ID).Error; e != nil || string(unknown.Ciphertext) != "private-input-sentinel" || string(unknown.Snapshot) != "private-provider-handle-sentinel" {
		t.Fatal("uncertain state lost", e)
	}
	got, e = repo.Cancel(ctx, own, terminal.PublicID, 1)
	if e != nil || got.Revision != 1 || got.CancelRequested {
		t.Fatal("terminal state rewritten", got, e)
	}
	if _, e = repo.Cancel(ctx, task.Access{Session: f.adminSession, Administrator: true}, queued.PublicID, 2); !errors.Is(e, task.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = repo.Cancel(ctx, task.Access{Session: f.otherSession}, unknown.PublicID, 2); !errors.Is(e, task.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestTaskControlConcurrentCancellationCAS(t *testing.T) {
	f := taskTestFixture(t)
	r := f.row(t, 1, f.owner.ID, "remote_active")
	repo := f.st.TaskControl()
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, e := repo.Cancel(context.Background(), task.Access{Session: f.session}, r.PublicID, 1)
			results <- e
		}()
	}
	success := 0
	for range 8 {
		e := <-results
		if e == nil {
			success++
		} else if !errors.Is(e, task.ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes = %d, want one", success)
	}
}
func TestTaskControlNeverSelectsContentColumns(t *testing.T) {
	f := taskTestFixture(t)
	r := f.row(t, 1, f.owner.ID, "remote_active")
	queries := 0
	e := f.st.DB().Callback().Query().Before("gorm:query").Register("test-task-select", func(tx *gorm.DB) {
		if tx.Statement.Table != "background_jobs" {
			return
		}
		queries++
		selected := strings.Join(tx.Statement.Selects, ",")
		if selected == "" || strings.Contains(selected, "*") {
			t.Error("unbounded metadata projection")
		}
		for _, forbidden := range []string{"ciphertext", "snapshot", "claim_hash"} {
			if strings.Contains(selected, forbidden) {
				t.Errorf("metadata selected %s", forbidden)
			}
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	repo := f.st.TaskControl()
	a := task.Access{Session: f.session}
	ctx := context.Background()
	if _, e = repo.List(ctx, a, task.Query{}); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Read(ctx, a, r.PublicID); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Cancel(ctx, a, r.PublicID, 1); e != nil {
		t.Fatal(e)
	}
	if queries != 3 {
		t.Fatalf("queries=%d want 3", queries)
	}
}
