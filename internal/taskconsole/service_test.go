package taskconsole_test

import (
	"context"
	"errors"
	"minigateway/internal/access"
	control "minigateway/internal/foundation/contracts/control/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	"minigateway/internal/taskconsole"
	"testing"
)

type probe struct {
	calls    int
	sessions int
}

func (p *probe) SessionUser(string) (*control.User, error) {
	p.sessions++
	return &control.User{ID: 1, Role: control.RoleUser}, nil
}
func (p *probe) List(ctx context.Context, a task.Access, q task.Query) (task.Page, error) {
	p.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing bounded query context")
	}
	return task.Page{}, nil
}
func (p *probe) Read(context.Context, task.Access, string) (task.Task, error) {
	p.calls++
	return task.Task{}, nil
}
func (p *probe) Cancel(context.Context, task.Access, string, uint64) (task.Task, error) {
	p.calls++
	return task.Task{}, nil
}
func TestConsoleRateIsSharedByOwnerNotSessionOrScope(t *testing.T) {
	p := &probe{}
	s := taskconsole.New(p, p, access.NewMemoryLimiter())
	ctx := context.Background()
	for i := 0; i < 120; i++ {
		if _, e := s.List(ctx, task.Access{Session: string(rune(i + 100))}, task.Query{}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Read(ctx, task.Access{Session: "another-session"}, "id"); !errors.Is(e, task.ErrRateLimited) {
		t.Fatal(e)
	}
	if p.calls != 120 {
		t.Fatal("rate refusal reached repository")
	}
	if _, e := s.List(ctx, task.Access{Administrator: true}, task.Query{}); !errors.Is(e, task.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := s.Cancel(ctx, task.Access{Administrator: true}, "id", 1); !errors.Is(e, task.ErrForbidden) {
		t.Fatal(e)
	}
}
