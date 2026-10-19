package routeconsole_test

import (
	"context"
	"errors"
	"testing"

	"minigateway/internal/access"
	control "minigateway/internal/foundation/contracts/control/v1"
	routes "minigateway/internal/foundation/contracts/routes/v1"
	"minigateway/internal/routeconsole"
)

type routeProbe struct {
	user     control.User
	calls    int
	sessions int
	last     routes.Scope
}

func (p *routeProbe) SessionUser(string) (*control.User, error) {
	p.sessions++
	copied := p.user
	return &copied, nil
}
func (p *routeProbe) List(ctx context.Context, scope routes.Scope, _ routes.Query) (routes.Page, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		panic("query has no bounded context")
	}
	p.calls++
	p.last = scope
	return routes.Page{}, nil
}
func (p *routeProbe) Read(_ context.Context, scope routes.Scope, _ string) (routes.Trace, error) {
	p.calls++
	p.last = scope
	return routes.Trace{}, nil
}

func TestRoutingQueriesRefreshAuthorityAndShareOwnerRate(t *testing.T) {
	p := &routeProbe{user: control.User{ID: 42, Role: control.RoleAdmin, Status: "active"}}
	service := routeconsole.New(p, p, access.NewMemoryLimiter())
	ctx := context.Background()
	for i := 0; i < 120; i++ {
		admin := i%2 == 0
		_, err := service.List(ctx, routes.Access{Session: string(rune(100 + i)), Administrator: admin}, routes.Query{})
		if err != nil || p.last.UserID != 42 || p.last.Administrator != admin {
			t.Fatal(p.last, err)
		}
	}
	if _, err := service.Read(ctx, routes.Access{Session: "another-current-session"}, "id"); !errors.Is(err, routes.ErrRateLimited) || p.calls != 120 || p.sessions != 121 {
		t.Fatal("scope/session switched to evade the owner bucket", p, err)
	}
	p.user.Role = control.RoleUser
	if _, err := service.Read(ctx, routes.Access{Administrator: true}, "id"); !errors.Is(err, routes.ErrForbidden) || p.calls != 120 {
		t.Fatal("stale administrator authority", p, err)
	}
	p.user.Status = "disabled"
	if _, err := service.List(ctx, routes.Access{}, routes.Query{}); !errors.Is(err, routes.ErrUnauthorized) || p.calls != 120 {
		t.Fatal("disabled authority reached observations", p, err)
	}
}

func TestRoutingCanceledQueryDoesNotConsumeSessionOrReadCapability(t *testing.T) {
	p := &routeProbe{user: control.User{ID: 1, Role: control.RoleUser, Status: "active"}}
	service := routeconsole.New(p, p, access.NewMemoryLimiter())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.List(ctx, routes.Access{}, routes.Query{}); !errors.Is(err, context.Canceled) || p.sessions != 0 || p.calls != 0 {
		t.Fatal(p, err)
	}
}
