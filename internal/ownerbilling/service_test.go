package ownerbilling

import (
	"context"
	"errors"
	"testing"
	"time"

	"minigateway/internal/access"
	control "minigateway/internal/foundation/contracts/control/v1"
	ledger "minigateway/internal/foundation/contracts/ownerledger/v1"
)

type ownerSessions struct {
	user *control.User
	err  error
}

func (s *ownerSessions) SessionUser(string) (*control.User, error) { return s.user, s.err }

type ownerRepository struct{ calls int }

func (r *ownerRepository) Summary(ctx context.Context, _ ledger.Access) (ledger.Summary, error) {
	r.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		return ledger.Summary{}, errors.New("unbounded financial read")
	}
	return ledger.Summary{UnitScale: "1000000000"}, nil
}

func (r *ownerRepository) List(ctx context.Context, a ledger.Access, _ ledger.Query) (ledger.Page, error) {
	_, err := r.Summary(ctx, a)
	return ledger.Page{Data: []ledger.Reservation{}}, err
}

func (r *ownerRepository) Read(ctx context.Context, a ledger.Access, id string) (ledger.Detail, error) {
	_, err := r.Summary(ctx, a)
	return ledger.Detail{Reservation: ledger.Reservation{ID: id}}, err
}

func TestOwnerLedgerRefreshesSessionAndBoundsAllReads(t *testing.T) {
	repository := &ownerRepository{}
	sessions := &ownerSessions{user: &control.User{ID: 7, Role: "user", Status: "active"}}
	service := New(repository, sessions, access.NewMemoryLimiter())
	ctx := context.Background()
	a := ledger.Access{Session: "current-session"}
	if _, err := service.Summary(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, a, ledger.Query{}); err != nil {
		t.Fatal(err)
	}
	sessions.user.Role = control.RoleAdmin
	if _, err := service.Read(ctx, a, "record"); err != nil {
		t.Fatal(err)
	}
	sessions.user.Status = "disabled"
	if _, err := service.List(ctx, a, ledger.Query{}); !errors.Is(err, ledger.ErrUnauthorized) || repository.calls != 3 {
		t.Fatal("disabled user reached repository", repository.calls, err)
	}
	sessions.user.Status = "active"
	sessions.err = errors.New("session revoked")
	if _, err := service.Summary(ctx, a); !errors.Is(err, ledger.ErrUnauthorized) || repository.calls != 3 {
		t.Fatal("revoked session reached repository", repository.calls, err)
	}
	sessions.err = nil
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Read(canceled, a, "record"); !errors.Is(err, context.Canceled) || repository.calls != 3 {
		t.Fatal("canceled call reached repository", repository.calls, err)
	}
	if _, err := service.Summary(nil, a); !errors.Is(err, ledger.ErrUnavailable) {
		t.Fatal(err)
	}
	for _, broken := range []ledger.Service{New(nil, sessions, access.NewMemoryLimiter()), New(repository, nil, access.NewMemoryLimiter()), New(repository, sessions, nil)} {
		if _, err := broken.Summary(ctx, a); !errors.Is(err, ledger.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}

func TestOwnerLedgerRateLimitIsSharedAcrossMethodsAndSessions(t *testing.T) {
	repository := &ownerRepository{}
	sessions := &ownerSessions{user: &control.User{ID: 9, Role: control.RoleAdmin, Status: "active"}}
	service := New(repository, sessions, access.NewMemoryLimiter())
	for i := 0; i < 120; i++ {
		var err error
		switch i % 3 {
		case 0:
			_, err = service.Summary(context.Background(), ledger.Access{Session: "first"})
		case 1:
			_, err = service.List(context.Background(), ledger.Access{Session: "second"}, ledger.Query{})
		case 2:
			_, err = service.Read(context.Background(), ledger.Access{Session: "third"}, "record")
		}
		if err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := service.Summary(context.Background(), ledger.Access{Session: "fourth"}); !errors.Is(err, ledger.ErrRateLimited) || repository.calls != 120 {
		t.Fatal("session bypassed owner query limit", repository.calls, err)
	}
}
