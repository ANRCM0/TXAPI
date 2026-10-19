package usageconsole

import (
	"context"
	"errors"
	"testing"
	"time"

	"minigateway/internal/access"
	control "minigateway/internal/foundation/contracts/control/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

type historySessions struct {
	user *control.User
	err  error
}

func (s *historySessions) SessionUser(string) (*control.User, error) { return s.user, s.err }

type historyRepository struct{ calls int }

func (r *historyRepository) List(ctx context.Context, _ history.Access, _ history.Query) (history.Page, error) {
	r.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		return history.Page{}, errors.New("unbounded usage read")
	}
	return history.Page{Data: []history.Summary{}, Enabled: true}, nil
}

func (r *historyRepository) Read(ctx context.Context, a history.Access, id string) (history.Detail, error) {
	_, err := r.List(ctx, a, history.Query{})
	return history.Detail{Record: history.Summary{ID: id}}, err
}

func TestUsageConsoleRefreshesSessionAndKeepsOwnerAdminScopes(t *testing.T) {
	repository := &historyRepository{}
	sessions := &historySessions{user: &control.User{ID: 7, Role: "user", Status: "active"}}
	service := New(repository, sessions, access.NewMemoryLimiter())
	ctx := context.Background()
	owner := history.Access{Session: "session-owner"}
	if _, err := service.List(ctx, owner, history.Query{}); err != nil {
		t.Fatal(err)
	}
	admin := owner
	admin.Administrator = true
	if _, err := service.Read(ctx, admin, "usage-id"); !errors.Is(err, history.ErrForbidden) || repository.calls != 1 {
		t.Fatal("owner obtained admin repository scope", repository.calls, err)
	}
	sessions.user.Role = control.RoleAdmin
	if _, err := service.Read(ctx, admin, "usage-id"); err != nil {
		t.Fatal(err)
	}
	sessions.user.Status = "disabled"
	if _, err := service.List(ctx, owner, history.Query{}); !errors.Is(err, history.ErrUnauthorized) || repository.calls != 2 {
		t.Fatal("disabled session reached repository", repository.calls, err)
	}
	sessions.user.Status = "active"
	sessions.err = errors.New("revoked")
	if _, err := service.Read(ctx, owner, "usage-id"); !errors.Is(err, history.ErrUnauthorized) || repository.calls != 2 {
		t.Fatal("revoked session reached repository", repository.calls, err)
	}
	sessions.err = nil
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Read(canceled, owner, "usage-id"); !errors.Is(err, context.Canceled) || repository.calls != 2 {
		t.Fatal("canceled read reached repository", repository.calls, err)
	}
}

func TestUsageConsoleSharesOwnerQueryLimitAcrossSessionsAndMethods(t *testing.T) {
	repository := &historyRepository{}
	sessions := &historySessions{user: &control.User{ID: 9, Role: control.RoleAdmin, Status: "active"}}
	service := New(repository, sessions, access.NewMemoryLimiter())
	for i := 0; i < 120; i++ {
		var err error
		if i%2 == 0 {
			_, err = service.List(context.Background(), history.Access{Session: "session-first"}, history.Query{})
		} else {
			_, err = service.Read(context.Background(), history.Access{Session: "session-second", Administrator: true}, "usage-id")
		}
		if err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := service.Read(context.Background(), history.Access{Session: "session-third"}, "usage-id"); !errors.Is(err, history.ErrRateLimited) || repository.calls != 120 {
		t.Fatal("new session bypassed shared owner bucket", repository.calls, err)
	}
}
