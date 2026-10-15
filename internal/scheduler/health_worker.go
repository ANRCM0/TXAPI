package scheduler

import (
	"log/slog"
	dbmodel "minigateway/internal/model"
	"sync"
	"time"
)

const healthQueueSize = 256

type healthEvent struct {
	credential dbmodel.Credential
	statusCode int
	message    string
	now        time.Time
	success    bool
}
type HealthPersister func(HealthUpdate) error
type HealthRefresh func(uint) (*dbmodel.Credential, error)

// HealthWorker serializes observations, computes from current storage, and drains
// accepted events on Close. No credential history is retained after processing.
// Supply refresh in production; without it, isolated consumers own snapshot freshness.
type HealthWorker struct {
	mu      sync.RWMutex
	closed  bool
	queue   chan healthEvent
	done    chan struct{}
	once    sync.Once
	persist HealthPersister
	reload  func()
	refresh HealthRefresh
}

func NewHealthWorker(persist HealthPersister, reload func(), refresh ...HealthRefresh) *HealthWorker {
	h := &HealthWorker{persist: persist, reload: reload, done: make(chan struct{})}
	if len(refresh) > 0 {
		h.refresh = refresh[0]
	}
	if persist == nil {
		close(h.done)
		return h
	}
	h.queue = make(chan healthEvent, healthQueueSize)
	go h.run()
	return h
}
func (h *HealthWorker) submit(event healthEvent) {
	if h == nil || event.credential.ID == 0 {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed || h.queue == nil {
		return
	}
	select {
	case h.queue <- event:
	default:
		slog.Warn("credential health queue full; update dropped", "credential_id", event.credential.ID)
	}
}
func (h *HealthWorker) run() {
	defer close(h.done)
	for event := range h.queue {
		h.process(event)
	}
}
func (h *HealthWorker) process(event healthEvent) {
	defer func() {
		if recover() != nil {
			slog.Error("credential health callback panicked", "credential_id", event.credential.ID)
		}
	}()
	current := event.credential
	if h.refresh != nil {
		latest, err := h.refresh(current.ID)
		if err != nil {
			slog.Warn("credential health refresh failed", "credential_id", current.ID, "err", err)
			return
		}
		if latest == nil {
			return
		}
		current = *latest
	}
	if current.HealthGeneration != event.credential.HealthGeneration || current.APIKey != event.credential.APIKey {
		return
	}
	if current.Status == dbmodel.CredentialDisabled || current.Status == dbmodel.CredentialBanned {
		return
	}
	// A late success that started before a newer failure must not undo cooling.
	if !event.credential.HealthObserved && event.success && current.FailCount > event.credential.FailCount {
		return
	}
	next := NextFailure(&current, event.statusCode, event.message, event.now)
	if event.success {
		next = NextSuccess(&current, event.now)
	}
	if event.credential.HealthObserved {
		// The coordinator already serialized and calculated this transition.
		// Persist that exact state, not a second calculation from a lagging DB.
		observed := event.credential
		if observed.HealthObservedAt <= current.HealthObservedAt {
			return
		}
		next.FailCount, next.Status, next.CooldownUntil = observed.FailCount, observed.Status, observed.CooldownUntil
		next.LastError, next.LastUsedAt = observed.LastError, observed.LastUsedAt
		next.ObservedAt = observed.HealthObservedAt
	} else {
		next.ObservedAt = event.now.UnixNano()
		if next.ObservedAt <= current.HealthObservedAt {
			next.ObservedAt = current.HealthObservedAt + 1
		}
	}
	if !next.Changed(&current) && !event.credential.HealthObserved {
		return
	}
	if err := h.persist(next); err != nil {
		slog.Warn("credential health persistence failed", "credential_id", current.ID, "err", err)
		return
	}
	if h.reload != nil {
		h.reload()
	}
}
func (h *HealthWorker) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.mu.Lock()
		h.closed = true
		if h.queue != nil {
			close(h.queue)
		}
		h.mu.Unlock()
		<-h.done
	})
}
