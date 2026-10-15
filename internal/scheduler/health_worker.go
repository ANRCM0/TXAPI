package scheduler

import (
	"log/slog"
	"sync"

	dbmodel "minigateway/internal/model"
)

const healthQueueSize = 256

type HealthPersister func(HealthUpdate) error

// HealthWorker owns one application's async health pipeline. Callbacks are
// immutable after construction; no process-global persister/reloader exists.
// Close drains accepted updates and is safe to call repeatedly/concurrently.
// Stop request producers before closing it, and close it before the store.
type HealthWorker struct {
	mu      sync.RWMutex
	closed  bool
	queue   chan HealthUpdate
	done    chan struct{}
	once    sync.Once
	persist HealthPersister
	reload  func()
}

func NewHealthWorker(persist HealthPersister, reload func()) *HealthWorker {
	h := &HealthWorker{persist: persist, reload: reload, done: make(chan struct{})}
	if persist == nil {
		close(h.done)
		return h
	}
	h.queue = make(chan HealthUpdate, healthQueueSize)
	go h.run()
	return h
}
func (h *HealthWorker) submit(next HealthUpdate, from *dbmodel.Credential) {
	if h == nil || !next.Changed(from) {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed || h.queue == nil {
		return
	}
	select {
	case h.queue <- next:
	default:
		slog.Warn("credential health queue full; update dropped", "credential_id", next.CredentialID)
	}
}
func (h *HealthWorker) run() {
	defer close(h.done)
	for update := range h.queue {
		batch := []HealthUpdate{update}
		for len(batch) < 64 {
			select {
			case next, ok := <-h.queue:
				if ok {
					batch = append(batch, next)
					continue
				}
			default:
			}
			break
		}
		for _, next := range batch {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("credential health persister panicked", "credential_id", next.CredentialID)
					}
				}()
				if err := h.persist(next); err != nil {
					slog.Warn("credential health persistence failed", "credential_id", next.CredentialID, "err", err)
				}
			}()
		}
		if h.reload != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("credential pool reloader panicked")
					}
				}()
				h.reload()
			}()
		}
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
