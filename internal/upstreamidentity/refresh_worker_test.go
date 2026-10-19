package upstreamidentity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

type workerManager struct {
	auth.Manager
	accounts []auth.Account
	list     func(context.Context, int, int) (auth.Page, error)
	refresh  func(context.Context, string, int64) (auth.Account, error)
	attempts []string
	starts   []time.Time
	now      func() time.Time
}

func (m *workerManager) List(ctx context.Context, limit, offset int) (auth.Page, error) {
	if m.list != nil {
		return m.list(ctx, limit, offset)
	}
	if offset >= len(m.accounts) {
		return auth.Page{}, nil
	}
	end := offset + limit
	if end > len(m.accounts) {
		end = len(m.accounts)
	}
	return auth.Page{Data: append([]auth.Account(nil), m.accounts[offset:end]...), HasMore: end < len(m.accounts)}, nil
}
func (m *workerManager) Refresh(ctx context.Context, id string, revision int64) (auth.Account, error) {
	m.attempts = append(m.attempts, id)
	if m.now != nil {
		m.starts = append(m.starts, m.now())
	}
	if m.refresh != nil {
		return m.refresh(ctx, id, revision)
	}
	return auth.Account{}, nil
}
func workerAccounts(n int, now time.Time) []auth.Account {
	accounts := make([]auth.Account, n)
	for i := range accounts {
		accounts[i] = auth.Account{ID: fmt.Sprintf("ua_%032x", i+1), Provider: "openai", Revision: 1, CredentialRevision: 1, State: auth.Active, HasRefresh: true, ExpiresAt: now.Add(time.Minute)}
	}
	return accounts
}
func testRefreshWorker(t *testing.T, m *workerManager, max int) (*RefreshWorker, *time.Time, *int) {
	t.Helper()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	publications := 0
	w, err := newRefreshWorker(RefreshWorkerOptions{Manager: m, Publish: func(context.Context) error { publications++; return nil }, MaxAttempts: max})
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return now }
	w.wait = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		if d > 0 {
			now = now.Add(d)
		}
		return true
	}
	m.now = w.now
	return w, &now, &publications
}

func TestRefreshWorkerValidatesOptions(t *testing.T) {
	m := &workerManager{}
	valid := RefreshWorkerOptions{Manager: m, Publish: func(context.Context) error { return nil }}
	var typedNil *workerManager
	for _, mutate := range []func(*RefreshWorkerOptions){
		func(o *RefreshWorkerOptions) { o.Manager = nil }, func(o *RefreshWorkerOptions) { o.Manager = typedNil }, func(o *RefreshWorkerOptions) { o.Publish = nil },
		func(o *RefreshWorkerOptions) { o.Interval = -1 }, func(o *RefreshWorkerOptions) { o.Interval = 4 * time.Second }, func(o *RefreshWorkerOptions) { o.Interval = time.Hour + 1 },
		func(o *RefreshWorkerOptions) { o.BeforeExpiry = 29 * time.Second }, func(o *RefreshWorkerOptions) { o.BeforeExpiry = 24*time.Hour + 1 },
		func(o *RefreshWorkerOptions) { o.Interval = time.Minute; o.BeforeExpiry = 30 * time.Second }, func(o *RefreshWorkerOptions) { o.MaxAttempts = -1 }, func(o *RefreshWorkerOptions) { o.MaxAttempts = 17 },
	} {
		o := valid
		mutate(&o)
		if _, err := NewRefreshWorker(context.Background(), o); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("invalid worker accepted")
		}
	}
	if _, err := NewRefreshWorker(nil, valid); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("nil parent accepted")
	}
	w, err := NewRefreshWorker(context.Background(), valid)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(nil); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("nil close context accepted")
	}
}

func TestRefreshWorkerFullPaginationFairnessAndRateAcrossCycles(t *testing.T) {
	m := &workerManager{}
	w, now, pubs := testRefreshWorker(t, m, 2)
	m.accounts = workerAccounts(203, *now)
	w.cycle(context.Background())
	if !reflect.DeepEqual(m.attempts, []string{m.accounts[0].ID, m.accounts[1].ID}) || *pubs != 3 {
		t.Fatalf("first cycle: attempts=%v publications=%d", m.attempts, *pubs)
	}
	// Include later pages in the rotation without rescanning only the first page.
	w.lastAccount = m.accounts[199].ID
	w.cycle(context.Background())
	if m.attempts[2] != m.accounts[200].ID || m.attempts[3] != m.accounts[201].ID {
		t.Fatal("later page starved")
	}
	w.cycle(context.Background())
	if m.attempts[4] != m.accounts[202].ID || m.attempts[5] != m.accounts[2].ID {
		t.Fatal("rotation did not wrap or repeated an account too soon")
	}
	for i := 1; i < len(m.starts); i++ {
		if m.starts[i].Sub(m.starts[i-1]) < time.Second {
			t.Fatal("provider rate exceeded across cycles")
		}
	}
	if len(w.lastAttempts) > len(m.accounts) {
		t.Fatal("unbounded attempt metadata")
	}
	m.accounts = m.accounts[:1]
	w.cycle(context.Background())
	if len(w.lastAttempts) != 1 {
		t.Fatal("retired account timing retained")
	}
}

func TestRefreshWorkerRejectsIncompleteScanBeforeAnySideEffect(t *testing.T) {
	for _, name := range []string{"duplicate", "invalid revision", "oversized page", "more beyond bound", "later page failure", "short continued page"} {
		t.Run(name, func(t *testing.T) {
			m := &workerManager{}
			w, now, pubs := testRefreshWorker(t, m, 2)
			m.accounts = workerAccounts(101, *now)
			switch name {
			case "duplicate":
				m.accounts[100] = m.accounts[0]
			case "invalid revision":
				m.accounts[100].CredentialRevision = 0
			case "oversized page":
				m.list = func(context.Context, int, int) (auth.Page, error) { return auth.Page{Data: m.accounts}, nil }
			case "more beyond bound":
				m.accounts = workerAccounts(1001, *now)
			case "later page failure":
				m.list = func(_ context.Context, _ int, offset int) (auth.Page, error) {
					if offset == 0 {
						return auth.Page{Data: m.accounts[:100], HasMore: true}, nil
					}
					return auth.Page{}, auth.ErrUnavailable
				}
			case "short continued page":
				m.list = func(context.Context, int, int) (auth.Page, error) {
					return auth.Page{Data: m.accounts[:1], HasMore: true}, nil
				}
			}
			w.cycle(context.Background())
			if len(m.attempts) != 0 || *pubs != 0 {
				t.Fatal("partial scan caused refresh/publication")
			}
		})
	}
}

func TestRefreshWorkerPublicationFailureRepairsBeforeMoreRefresh(t *testing.T) {
	m := &workerManager{}
	w, now, _ := testRefreshWorker(t, m, 4)
	m.accounts = workerAccounts(3, *now)
	calls := 0
	fail := true
	w.opts.Publish = func(context.Context) error {
		calls++
		if fail && calls >= 2 {
			return errors.New("private detail")
		}
		return nil
	}
	w.cycle(context.Background())
	if len(m.attempts) != 1 || !w.pendingPublication {
		t.Fatal("publication failure did not stop refresh")
	}
	w.cycle(context.Background())
	if len(m.attempts) != 1 {
		t.Fatal("failed repair repeated refresh")
	}
	fail = false
	w.cycle(context.Background())
	if len(m.attempts) != 3 || w.pendingPublication {
		t.Fatal("publication repair did not resume other accounts")
	}
	if m.attempts[0] == m.attempts[1] {
		t.Fatal("repair repeated completed attempt")
	}
}

func TestRefreshWorkerMetadataChangesPublishBeforeRefresh(t *testing.T) {
	m := &workerManager{}
	w, now, pubs := testRefreshWorker(t, m, 1)
	w.cycle(context.Background())
	if *pubs != 0 {
		t.Fatal("empty initial scan published")
	}
	m.accounts = workerAccounts(1, *now)
	m.accounts[0].ExpiresAt = now.Add(time.Hour)
	w.cycle(context.Background())
	w.cycle(context.Background())
	if *pubs != 1 {
		t.Fatal("unchanged revision metadata repeatedly published")
	}
	m.accounts[0].CredentialRevision++
	w.cycle(context.Background())
	if *pubs != 2 {
		t.Fatal("manual credential rotation not published")
	}
	m.accounts = nil
	w.cycle(context.Background())
	if *pubs != 3 {
		t.Fatal("removed account not published")
	}
	m.accounts = workerAccounts(1, *now)
	w.opts.Publish = func(context.Context) error { return auth.ErrUnavailable }
	w.cycle(context.Background())
	if len(m.attempts) != 0 || !w.pendingPublication {
		t.Fatal("failed initial publication allowed refresh")
	}
}

func TestRefreshWorkerScanCancellationDiscardsCompleteEarlierPages(t *testing.T) {
	m := &workerManager{}
	w, now, pubs := testRefreshWorker(t, m, 1)
	m.accounts = workerAccounts(100, *now)
	ctx, cancel := context.WithCancel(context.Background())
	m.list = func(ctx context.Context, _, offset int) (auth.Page, error) {
		if offset == 0 {
			return auth.Page{Data: m.accounts, HasMore: true}, nil
		}
		cancel()
		return auth.Page{}, ctx.Err()
	}
	w.cycle(ctx)
	if len(m.attempts) != 0 || *pubs != 0 || w.seen {
		t.Fatal("canceled partial scan affected state")
	}
}

func TestRefreshWorkerLoopWaitsAfterCompletedCycle(t *testing.T) {
	m := &workerManager{}
	w, _, _ := testRefreshWorker(t, m, 1)
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	scans, waits := 0, 0
	m.list = func(context.Context, int, int) (auth.Page, error) { scans++; return auth.Page{}, nil }
	w.wait = func(_ context.Context, delay time.Duration) bool {
		waits++
		if delay != w.opts.Interval || scans != waits {
			t.Fatal("loop caught up or waited before initial scan")
		}
		if waits == 2 {
			cancel()
			return false
		}
		return true
	}
	w.run(ctx)
	if scans != 2 || waits != 2 {
		t.Fatal("loop pacing changed")
	}
}

func TestRefreshWorkerEligibilityAndPerAccountMinimum(t *testing.T) {
	m := &workerManager{}
	w, now, _ := testRefreshWorker(t, m, 16)
	m.accounts = workerAccounts(8, *now)
	m.accounts[0].Provider = "other"
	m.accounts[1].State = auth.Uncertain
	m.accounts[2].State = auth.Reauthorize
	m.accounts[3].State = auth.Disabled
	m.accounts[4].HasRefresh = false
	m.accounts[5].NextRefreshAt = now.Add(time.Hour)
	m.accounts[6].ExpiresAt = now.Add(time.Hour)
	w.cycle(context.Background())
	w.cycle(context.Background())
	if !reflect.DeepEqual(m.attempts, []string{m.accounts[7].ID}) {
		t.Fatal("ineligible or too-recent account refreshed")
	}
	*now = now.Add(time.Minute)
	w.cycle(context.Background())
	if len(m.attempts) != 2 {
		t.Fatal("minimum interval never reopened")
	}
}

func TestRefreshWorkerUncertainOutcomeNeverRetriesFromOldMetadata(t *testing.T) {
	m := &workerManager{}
	w, now, _ := testRefreshWorker(t, m, 1)
	m.accounts = workerAccounts(1, *now)
	m.refresh = func(context.Context, string, int64) (auth.Account, error) {
		m.accounts[0].State = auth.Uncertain
		m.accounts[0].Revision++
		return auth.Account{}, auth.ErrUncertain
	}
	w.cycle(context.Background())
	*now = now.Add(2 * time.Minute)
	w.cycle(context.Background())
	if len(m.attempts) != 1 {
		t.Fatal("uncertain state automatically replayed")
	}
}

func TestRefreshWorkerProviderFailureCooldownAndSkippedConflicts(t *testing.T) {
	m := &workerManager{}
	w, now, _ := testRefreshWorker(t, m, 16)
	m.accounts = workerAccounts(6, *now)
	failures := []error{auth.ErrUnavailable, auth.ErrConflict, auth.ErrUnavailable, auth.ErrUncertain, nil, nil}
	m.refresh = func(context.Context, string, int64) (auth.Account, error) {
		if len(m.attempts) <= len(failures) {
			return auth.Account{}, failures[len(m.attempts)-1]
		}
		return auth.Account{}, nil
	}
	w.cycle(context.Background())
	if len(m.attempts) != 4 {
		t.Fatal("consecutive provider failure gate wrong")
	}
	w.cycle(context.Background())
	if len(m.attempts) != 4 {
		t.Fatal("provider cooldown ignored")
	}
	*now = now.Add(time.Minute)
	w.cycle(context.Background())
	if len(m.attempts) < 6 || m.attempts[4] != m.accounts[4].ID {
		t.Fatal("cooldown lost fair rotation")
	}
}

func TestRefreshWorkerFailureThresholdPersistsAcrossCycles(t *testing.T) {
	m := &workerManager{}
	w, now, _ := testRefreshWorker(t, m, 1)
	m.accounts = workerAccounts(4, *now)
	m.refresh = func(context.Context, string, int64) (auth.Account, error) { return auth.Account{}, auth.ErrUnavailable }
	w.cycle(context.Background())
	w.cycle(context.Background())
	if len(m.attempts) != 2 || !w.now().Before(w.cooldownUntil) {
		t.Fatal("separate-cycle failures did not cool provider")
	}
	w.cycle(context.Background())
	if len(m.attempts) != 2 {
		t.Fatal("cooldown allowed another attempt")
	}
	*now = now.Add(time.Minute)
	w.cycle(context.Background())
	if len(m.attempts) != 3 || m.attempts[2] != m.accounts[2].ID {
		t.Fatal("provider did not resume fairly after cooldown")
	}
}

func TestRefreshWorkerPanicRepairsPublicationAndContinues(t *testing.T) {
	for _, location := range []string{"scan", "refresh", "publish"} {
		t.Run(location, func(t *testing.T) {
			m := &workerManager{}
			w, now, _ := testRefreshWorker(t, m, 1)
			m.accounts = workerAccounts(2, *now)
			panicOnce := true
			switch location {
			case "scan":
				m.list = func(context.Context, int, int) (auth.Page, error) {
					if panicOnce {
						panicOnce = false
						panic("private scan detail")
					}
					return auth.Page{Data: m.accounts}, nil
				}
			case "refresh":
				m.refresh = func(context.Context, string, int64) (auth.Account, error) {
					if panicOnce {
						panicOnce = false
						panic("private token detail")
					}
					return auth.Account{}, nil
				}
			case "publish":
				w.opts.Publish = func(context.Context) error {
					if panicOnce {
						panicOnce = false
						panic("private publication detail")
					}
					return nil
				}
			}
			w.cycle(context.Background())
			if !w.pendingPublication || !w.now().Before(w.cooldownUntil) {
				t.Fatal("panic did not retain repair/cooldown")
			}
			before := len(m.attempts)
			w.cycle(context.Background())
			if w.pendingPublication || len(m.attempts) != before {
				t.Fatal("repair failed or refreshed during cooldown")
			}
			*now = now.Add(time.Minute)
			w.cycle(context.Background())
			if len(m.attempts) != before+1 {
				t.Fatal("cycle permanently stopped after panic")
			}
		})
	}
}

func TestRefreshWorkerContextBudgetsAndCancellation(t *testing.T) {
	m := &workerManager{}
	w, now, pubs := testRefreshWorker(t, m, 1)
	m.accounts = workerAccounts(1, *now)
	m.list = func(ctx context.Context, limit, offset int) (auth.Page, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || limit != 100 || offset != 0 {
			t.Fatal("scan budget missing")
		}
		return auth.Page{Data: m.accounts}, nil
	}
	m.refresh = func(ctx context.Context, _ string, _ int64) (auth.Account, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("attempt budget missing")
		}
		return auth.Account{}, nil
	}
	w.opts.Publish = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute {
			t.Fatal("cycle budget missing")
		}
		return nil
	}
	w.cycle(context.Background())
	if len(m.attempts) != 1 {
		t.Fatal("budgeted attempt did not run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.cycle(ctx)
	if len(m.attempts) != 1 || *pubs != 0 {
		t.Fatal("canceled cycle performed I/O")
	}
}

func TestRefreshWorkerCloseRetainsOwnershipUntilManagerJoins(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m := &workerManager{accounts: workerAccounts(1, time.Now())}
	var once sync.Once
	m.refresh = func(ctx context.Context, _ string, _ int64) (auth.Account, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		<-release
		return auth.Account{}, ctx.Err()
	}
	w, err := NewRefreshWorker(context.Background(), RefreshWorkerOptions{Manager: m, Publish: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initial cycle did not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err = w.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Close falsely joined manager")
	}
	close(release)
	if err = w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(context.Background()); err != nil {
		t.Fatal("Close not repeatable")
	}
	if len(m.attempts) != 1 {
		t.Fatal("cancel restarted refresh")
	}
}
