package upstreamidentity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"time"

	lifecycle "minigateway/internal/foundation/contracts/lifecycle/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

// RefreshWorkerOptions configures the existing account manager's bounded scan.
// Publish must reload account metadata through the normal configuration owner.
type RefreshWorkerOptions struct {
	Manager      auth.Manager
	Publish      func(context.Context) error
	Interval     time.Duration
	BeforeExpiry time.Duration
	MaxAttempts  int
}

// RefreshWorker owns one serial loop, but never owns its manager/dependencies.
type RefreshWorker struct {
	opts                RefreshWorkerOptions
	cancel              context.CancelFunc
	done                chan struct{}
	now                 func() time.Time
	wait                func(context.Context, time.Duration) bool
	seen                bool
	digest              [32]byte
	pendingPublication  bool
	lastAccount         string
	lastStarted         time.Time
	cooldownUntil       time.Time
	consecutiveFailures int
	lastAttempts        map[string]time.Time
}

var _ lifecycle.Drainer = (*RefreshWorker)(nil)

func newRefreshWorker(opts RefreshWorkerOptions) (*RefreshWorker, error) {
	if opts.Manager == nil || opts.Publish == nil {
		return nil, auth.ErrInvalid
	}
	v := reflect.ValueOf(opts.Manager)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Chan, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil, auth.ErrInvalid
		}
	}
	if opts.Interval == 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.BeforeExpiry == 0 {
		opts.BeforeExpiry = 10 * time.Minute
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 4
	}
	if opts.Interval < 5*time.Second || opts.Interval > time.Hour || opts.BeforeExpiry < 30*time.Second || opts.BeforeExpiry > 24*time.Hour || opts.BeforeExpiry < opts.Interval || opts.MaxAttempts < 1 || opts.MaxAttempts > 16 {
		return nil, auth.ErrInvalid
	}
	return &RefreshWorker{opts: opts, done: make(chan struct{}), now: time.Now, wait: waitRefresh, lastAttempts: make(map[string]time.Time)}, nil
}

func NewRefreshWorker(parent context.Context, opts RefreshWorkerOptions) (*RefreshWorker, error) {
	if parent == nil || parent.Err() != nil {
		return nil, auth.ErrInvalid
	}
	w, err := newRefreshWorker(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	go w.run(ctx)
	return w, nil
}

func waitRefresh(ctx context.Context, duration time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if duration <= 0 {
		return true
	}
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}

func (w *RefreshWorker) run(ctx context.Context) {
	defer close(w.done)
	for ctx.Err() == nil {
		w.cycle(ctx)
		if !w.wait(ctx, w.opts.Interval) {
			return
		}
	}
}

// Close may time out while the manager commits its cancellation outcome. The
// owner must retain dependencies and call Close again until it joins.
func (w *RefreshWorker) Close(ctx context.Context) error {
	if ctx == nil {
		return auth.ErrInvalid
	}
	if w == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	default:
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validRefreshMetadata(a auth.Account) bool {
	if !auth.ValidAccountID(a.ID) || !auth.ValidWireText(a.Provider, 128) || a.Revision <= 0 || a.CredentialRevision <= 0 {
		return false
	}
	switch a.State {
	case auth.Active, auth.Disabled, auth.Reauthorize, auth.Uncertain:
	default:
		return false
	}
	return !a.ExpiresAt.IsZero()
}

func (w *RefreshWorker) scan(ctx context.Context) ([]auth.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	accounts := make([]auth.Account, 0, 100)
	seen := make(map[string]struct{})
	for page := 0; page < 10; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := w.opts.Manager.List(ctx, 100, page*100)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(result.Data) > 100 || result.HasMore && (len(result.Data) != 100 || page == 9) {
			return nil, auth.ErrInvalid
		}
		for _, account := range result.Data {
			if _, duplicate := seen[account.ID]; duplicate || !validRefreshMetadata(account) {
				return nil, auth.ErrInvalid
			}
			seen[account.ID] = struct{}{}
			accounts = append(accounts, account)
		}
		if !result.HasMore {
			return accounts, nil
		}
	}
	return nil, auth.ErrInvalid
}

func refreshDigest(accounts []auth.Account) [32]byte {
	h := sha256.New()
	var revisions [16]byte
	for _, a := range accounts {
		h.Write([]byte(a.ID))
		binary.BigEndian.PutUint64(revisions[:8], uint64(a.Revision))
		binary.BigEndian.PutUint64(revisions[8:], uint64(a.CredentialRevision))
		h.Write(revisions[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (w *RefreshWorker) publish(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if err := w.opts.Publish(ctx); err != nil {
		slog.Warn("account refresh publication pending")
		return false
	}
	w.pendingPublication = false
	return ctx.Err() == nil
}

func (w *RefreshWorker) refresh(ctx context.Context, account auth.Account) error {
	attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := w.opts.Manager.Refresh(attempt, account.ID, account.Revision)
	return err
}

func (w *RefreshWorker) cycle(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	defer func() {
		if recover() != nil {
			// A dependency may have committed before panicking. Repair publication
			// on the next complete scan; do not infer or replay its wire outcome.
			w.pendingPublication = true
			w.consecutiveFailures = 0
			w.cooldownUntil = w.now().Add(time.Minute)
			slog.Error("account refresh cycle interrupted")
		}
	}()
	accounts, err := w.scan(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("account refresh scan unavailable")
		}
		return
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	digest := refreshDigest(accounts)
	if (!w.seen && len(accounts) > 0) || (w.seen && digest != w.digest) {
		w.pendingPublication = true
	}
	w.seen, w.digest = true, digest
	current := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		current[a.ID] = struct{}{}
	}
	for id := range w.lastAttempts {
		if _, ok := current[id]; !ok {
			delete(w.lastAttempts, id)
		}
	}
	if w.pendingPublication && !w.publish(ctx) {
		return
	}
	if ctx.Err() != nil || w.now().Before(w.cooldownUntil) {
		return
	}
	start := sort.Search(len(accounts), func(i int) bool { return accounts[i].ID > w.lastAccount })
	if start == len(accounts) {
		start = 0
	}
	attempts := 0
	for offset := 0; offset < len(accounts) && attempts < w.opts.MaxAttempts; offset++ {
		a := accounts[(start+offset)%len(accounts)]
		now := w.now()
		if a.Provider != "openai" || a.State != auth.Active || !a.HasRefresh || a.ExpiresAt.After(now.Add(w.opts.BeforeExpiry)) || a.NextRefreshAt.After(now) {
			continue
		}
		if previous, ok := w.lastAttempts[a.ID]; ok && now.Before(previous.Add(time.Minute)) {
			continue
		}
		if !w.lastStarted.IsZero() && !w.wait(ctx, w.lastStarted.Add(time.Second).Sub(now)) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		w.lastAccount, w.lastStarted = a.ID, w.now()
		w.lastAttempts[a.ID] = w.lastStarted
		attempts++
		err := w.refresh(ctx, a)
		w.pendingPublication = true
		cooling := false
		if errors.Is(err, auth.ErrUnavailable) || errors.Is(err, auth.ErrUncertain) || errors.Is(err, context.DeadlineExceeded) {
			w.consecutiveFailures++
			if w.consecutiveFailures >= 2 {
				w.consecutiveFailures = 0
				w.cooldownUntil = w.now().Add(time.Minute)
				cooling = true
				slog.Warn("account refresh provider cooling down")
			}
		} else {
			w.consecutiveFailures = 0
		}
		if !w.publish(ctx) {
			return
		}
		if ctx.Err() != nil || cooling {
			return
		}
	}
}
