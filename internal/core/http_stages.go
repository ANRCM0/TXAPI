package core

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// HTTPStageTimeouts bounds waits within one existing HTTP attempt. Zero values
// retain the legacy policy. No stage can extend the attempt/parent deadline.
type HTTPStageTimeouts struct {
	Headers              time.Duration
	FirstEffectiveOutput time.Duration
	StreamIdle           time.Duration
	DownstreamWrite      time.Duration
}

type TimeoutStage string

const (
	TimeoutHeaders              TimeoutStage = "headers"
	TimeoutFirstEffectiveOutput TimeoutStage = "first_effective_output"
	TimeoutStreamIdle           TimeoutStage = "stream_idle"
	TimeoutDownstreamWrite      TimeoutStage = "downstream_write"
)

func (s HTTPStageTimeouts) enabled() bool {
	return s.Headers > 0 || s.FirstEffectiveOutput > 0 || s.StreamIdle > 0 || s.DownstreamWrite > 0
}

func (s HTTPStageTimeouts) valid() bool {
	return s.Headers >= 0 && s.FirstEffectiveOutput >= 0 && s.StreamIdle >= 0 && s.DownstreamWrite >= 0
}

// The executor remains the only reader/writer. This single watchdog cancels its
// context and is always joined before ExecuteTo returns. All timer transitions
// and expiry decisions share the same lock; no callback can mutate Result.
type httpStageWatchdog struct {
	mu                         sync.Mutex
	ctx                        context.Context
	cancel                     context.CancelFunc
	budgets                    HTTPStageTimeouts
	header, first, idle, write time.Time
	paused                     time.Time
	total                      time.Time
	fired                      TimeoutStage
	terminal                   bool
	parentCanceledBeforeWrite  bool
	wake, stop, done           chan struct{}
}

func newHTTPStageWatchdog(parent context.Context, budgets HTTPStageTimeouts) (context.Context, *httpStageWatchdog) {
	ctx, cancel := context.WithCancel(parent)
	w := &httpStageWatchdog{ctx: parent, cancel: cancel, budgets: budgets,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	w.total, _ = parent.Deadline()
	go w.run()
	return ctx, w
}

func (w *httpStageWatchdog) nextLocked() (time.Time, TimeoutStage) {
	var next time.Time
	var stage TimeoutStage
	choose := func(deadline time.Time, candidate TimeoutStage) {
		if !deadline.IsZero() && (next.IsZero() || deadline.Before(next)) {
			next, stage = deadline, candidate
		}
	}
	choose(w.header, TimeoutHeaders)
	if w.paused.IsZero() && !w.terminal {
		choose(w.first, TimeoutFirstEffectiveOutput)
		choose(w.idle, TimeoutStreamIdle)
	}
	choose(w.write, TimeoutDownstreamWrite)
	// A stage that cannot beat the outer deadline cannot claim its attribution.
	if !w.total.IsZero() && !next.IsZero() && !next.Before(w.total) {
		return time.Time{}, ""
	}
	return next, stage
}

func (w *httpStageWatchdog) expireLocked(now time.Time) {
	if w.fired != "" || w.ctx.Err() != nil || !w.total.IsZero() && !now.Before(w.total) {
		return
	}
	deadline, stage := w.nextLocked()
	if !deadline.IsZero() && !now.Before(deadline) {
		w.fired = stage
		w.cancel()
	}
}

func (w *httpStageWatchdog) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *httpStageWatchdog) run() {
	defer close(w.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		w.mu.Lock()
		w.expireLocked(time.Now())
		deadline, _ := w.nextLocked()
		finished := w.fired != ""
		w.mu.Unlock()
		if finished {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var tick <-chan time.Time
		if !deadline.IsZero() {
			timer.Reset(time.Until(deadline))
			tick = timer.C
		}
		select {
		case <-w.stop:
			return
		case <-w.ctx.Done():
			w.mu.Lock()
			if !w.write.IsZero() && time.Now().Before(w.write) {
				w.parentCanceledBeforeWrite = true
			}
			w.mu.Unlock()
			return
		case <-w.wake:
		case <-tick:
		}
	}
}

func (w *httpStageWatchdog) close() {
	if w == nil {
		return
	}
	close(w.stop)
	<-w.done
	w.cancel()
}

func (w *httpStageWatchdog) timeoutStage() TimeoutStage {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expireLocked(time.Now())
	return w.fired
}

func (w *httpStageWatchdog) headers(start bool, stream bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.expireLocked(now)
	if start {
		if w.budgets.Headers > 0 {
			w.header = now.Add(w.budgets.Headers)
		}
	} else {
		w.header = time.Time{}
		if stream {
			if w.budgets.FirstEffectiveOutput > 0 {
				w.first = now.Add(w.budgets.FirstEffectiveOutput)
			}
			if w.budgets.StreamIdle > 0 {
				w.idle = now.Add(w.budgets.StreamIdle)
			}
		}
	}
	w.signal()
}

func (w *httpStageWatchdog) progress(frame Frame) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.expireLocked(now)
	if w.fired != "" || w.ctx.Err() != nil {
		return false
	}
	if frame.EffectiveOutput || frame.Complete {
		w.first = time.Time{}
	}
	if frame.EventComplete && w.budgets.StreamIdle > 0 {
		w.idle = now.Add(w.budgets.StreamIdle)
	}
	if frame.Complete {
		w.terminal = true
		w.first, w.idle = time.Time{}, time.Time{}
	}
	w.signal()
	return true
}

func (w *httpStageWatchdog) beginOutput() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.expireLocked(now)
	w.paused = now
	if w.budgets.DownstreamWrite > 0 {
		w.write = now.Add(w.budgets.DownstreamWrite)
	}
	w.signal()
}

func (w *httpStageWatchdog) endOutput() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.expireLocked(now)
	if !w.paused.IsZero() {
		pause := now.Sub(w.paused)
		if !w.first.IsZero() {
			w.first = w.first.Add(pause)
		}
		if !w.idle.IsZero() {
			w.idle = w.idle.Add(pause)
		}
	}
	w.paused, w.write = time.Time{}, time.Time{}
	w.signal()
}

// A native HTTP/2 write deadline may cancel the handler context before Write
// returns. Preserve the expired local write budget rather than reclassifying
// that local transport timeout as a client cancellation.
func (w *httpStageWatchdog) outputFailed(err error) {
	if w == nil {
		return
	}
	var timeout interface{ Timeout() bool }
	if !errors.Is(err, os.ErrDeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if !w.parentCanceledBeforeWrite && w.fired == "" && !w.write.IsZero() && !now.Before(w.write) && (w.total.IsZero() || w.write.Before(w.total)) {
		w.fired = TimeoutDownstreamWrite
		w.cancel()
	}
}
