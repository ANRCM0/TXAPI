package quoteconsole_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	control "minigateway/internal/foundation/contracts/control/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	q "minigateway/internal/foundation/contracts/quotation/v1"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/quoteconsole"
	"minigateway/internal/store"
)

type probe struct {
	user                control.User
	sessionErr          error
	sessionCalls, reads int
	last                q.Input
	err                 error
	onSession           func()
}

func (p *probe) SessionUser(string) (*control.User, error) {
	p.sessionCalls++
	if p.onSession != nil {
		p.onSession()
	}
	u := p.user
	return &u, p.sessionErr
}

func (p *probe) check(ctx context.Context) {
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 5*time.Second || ctx.Err() != nil {
		panic("reader context must be live and bounded")
	}
	p.reads++
}

func (p *probe) Current(ctx context.Context) (q.Current, error) {
	p.check(ctx)
	return q.Current{Enabled: true, Version: "pinned-version", Currency: "TEST"}, p.err
}

func (p *probe) Calculate(ctx context.Context, input q.Input) (q.Quote, error) {
	p.check(ctx)
	p.last = input
	return q.Quote{PriceVersion: input.PriceVersion, AmountUnits: "17", Known: true, Reason: "quoted"}, p.err
}

func adminProbe() *probe {
	return &probe{user: control.User{ID: 42, Role: control.RoleAdmin, Status: "active"}}
}

func TestQuotationConsoleSharesOwnerBucketAndRefreshesEachOperation(t *testing.T) {
	p := adminProbe()
	s := quoteconsole.New(p, p, access.NewMemoryLimiter())
	for i := 0; i < 120; i++ {
		var err error
		if i%2 == 0 {
			_, err = s.Current(context.Background(), q.Access{Session: "first"})
		} else {
			_, err = s.Calculate(context.Background(), q.Access{Session: "second"}, q.Input{})
		}
		if err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.Current(context.Background(), q.Access{Session: "third"}); !errors.Is(err, q.ErrRateLimited) || p.reads != 120 || p.sessionCalls != 121 {
		t.Fatal("session rotation bypassed owner limit", p, err)
	}
	p.user.Role = control.RoleUser
	if _, err := s.Calculate(context.Background(), q.Access{}, q.Input{}); !errors.Is(err, q.ErrForbidden) || p.reads != 120 {
		t.Fatal("non-admin acquired quotes", err)
	}
	p.user.Role = control.RoleAdmin
	p.user.Status = "disabled"
	if _, err := s.Current(context.Background(), q.Access{}); !errors.Is(err, q.ErrUnauthorized) || p.reads != 120 {
		t.Fatal("disabled user acquired quotes", err)
	}
}

func TestQuotationConsolePreservesTypedInputResultsAndErrors(t *testing.T) {
	p := adminProbe()
	s := quoteconsole.New(p, p, access.NewMemoryLimiter())
	zero := int64(0)
	input := q.Input{PriceVersion: "historical", Model: "exact-model", Group: "exact-group", Usage: v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: &zero}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "observed-profile"}}
	want := input
	got, err := s.Calculate(context.Background(), q.Access{}, input)
	if err != nil || got.AmountUnits != "17" || got.Reason != "quoted" || got.PriceVersion != "historical" || !reflect.DeepEqual(p.last, want) || !reflect.DeepEqual(input, want) {
		t.Fatal("quotation/provenance changed", got, p.last, err)
	}
	for _, typed := range []error{q.ErrInvalid, q.ErrNotFound, q.ErrUnavailable, q.ErrOverflow} {
		p.err = typed
		if _, err := s.Current(context.Background(), q.Access{}); err != typed {
			t.Fatal("reader current error changed", err)
		}
		if _, err := s.Calculate(context.Background(), q.Access{}, input); err != typed {
			t.Fatal("reader calculate error changed", err)
		}
	}
}

func TestQuotationConsoleRejectsMissingDependenciesAndCanceledContexts(t *testing.T) {
	p := adminProbe()
	for _, s := range []q.Service{quoteconsole.New(nil, p, access.NewMemoryLimiter()), quoteconsole.New(p, nil, access.NewMemoryLimiter()), quoteconsole.New(p, p, nil)} {
		if _, err := s.Current(context.Background(), q.Access{}); !errors.Is(err, q.ErrUnavailable) {
			t.Fatal(err)
		}
		if _, err := s.Calculate(context.Background(), q.Access{}, q.Input{}); !errors.Is(err, q.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	s := quoteconsole.New(p, p, access.NewMemoryLimiter())
	if _, err := s.Current(nil, q.Access{}); !errors.Is(err, q.ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Current(ctx, q.Access{}); !errors.Is(err, context.Canceled) || p.sessionCalls != 0 || p.reads != 0 {
		t.Fatal("canceled operation used dependencies", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	p.onSession = cancel
	if _, err := s.Calculate(ctx, q.Access{}, q.Input{}); !errors.Is(err, context.Canceled) || p.reads != 0 {
		t.Fatal("canceled authorization read backend", err)
	}
	p.onSession = nil
	p.sessionErr = errors.New("private session database failure")
	if _, err := s.Current(context.Background(), q.Access{}); err != q.ErrUnauthorized {
		t.Fatal("session error leaked", err)
	}
}

func TestQuotationConsoleRealSessionsRevokeCurrentAuthority(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "quotes.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	controlService := controlplane.New(persistence.Control(st))
	if _, err := controlService.CreateUser(&control.UserInput{Username: "spare", Password: "spare-password-123", Role: control.RoleAdmin, Status: "active", ModelMode: access.All}); err != nil {
		t.Fatal(err)
	}
	p := adminProbe()
	s := quoteconsole.New(p, controlService, access.NewMemoryLimiter())
	for _, change := range []string{"logout", "demotion", "disabled", "deleted"} {
		t.Run(change, func(t *testing.T) {
			u, err := controlService.CreateUser(&control.UserInput{Username: change, Password: "test-password-123", Role: control.RoleAdmin, Status: "active", ModelMode: access.All})
			if err != nil {
				t.Fatal(err)
			}
			secret, _, err := controlService.Login(change, "test-password-123")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Current(context.Background(), q.Access{Session: secret}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "logout":
				err = controlService.Logout(secret)
			case "deleted":
				err = st.DB().Exec("DELETE FROM users WHERE id = ?", u.ID).Error
			default:
				role, status := control.RoleAdmin, "active"
				if change == "demotion" {
					role = control.RoleUser
				} else {
					status = "disabled"
				}
				_, err = controlService.UpdateUser(u.ID, &control.UserInput{Username: change, Role: role, Status: status, ModelMode: access.All})
			}
			if err != nil {
				t.Fatal(err)
			}
			before := p.reads
			if _, err := s.Calculate(context.Background(), q.Access{Session: secret}, q.Input{}); err != q.ErrUnauthorized || p.reads != before {
				t.Fatal("revoked session reached quote reader", err)
			}
		})
	}
	user, err := controlService.CreateUser(&control.UserInput{Username: "ordinary", Password: "test-password-123", Role: control.RoleUser, Status: "active", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := controlService.Login(user.Username, "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(context.Background(), q.Access{Session: secret}); err != q.ErrForbidden {
		t.Fatal("active ordinary session accepted", err)
	}
}
