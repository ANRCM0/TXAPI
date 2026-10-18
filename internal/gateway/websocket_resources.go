package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

// These resources and factories stay private to the native host. A lease is
// only a lifetime pin; it never exposes a transport or mutable configuration.
type websocketResource interface{ Close(context.Context) error }
type websocketGeneration struct {
	revision string
	resource websocketResource
	refs     int
	closing  bool
	closeErr error
}
type websocketResources struct {
	mu        sync.Mutex
	current   *websocketGeneration
	candidate *websocketGeneration
	preparing bool
	draining  bool
	retired   map[string]*websocketGeneration
	changed   chan struct{}
}

func newWebSocketResources() *websocketResources {
	return &websocketResources{retired: make(map[string]*websocketGeneration), changed: make(chan struct{})}
}
func (o *websocketResources) signalLocked() { close(o.changed); o.changed = make(chan struct{}) }

// prepare reserves the one candidate slot before executing bounded factory I/O.
// A factory returning both resource and error transfers partial cleanup too.
func (o *websocketResources) prepare(ctx context.Context, revision string, factory func(context.Context) (websocketResource, error)) error {
	if ctx == nil || revision == "" || factory == nil {
		return errors.New("invalid resource candidate")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	if o.draining || o.preparing || o.candidate != nil || o.retired[revision] != nil {
		o.mu.Unlock()
		return errors.New("resource candidate unavailable")
	}
	if o.current != nil && o.current.revision == revision {
		o.mu.Unlock()
		return nil
	}
	o.preparing = true
	o.signalLocked()
	o.mu.Unlock()
	resource, err := factory(ctx)
	if err == nil && nilInterface(resource) {
		err = errors.New("candidate has no resources")
	}
	if err == nil {
		err = ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.preparing = false
	if !nilInterface(resource) {
		o.candidate = &websocketGeneration{revision: revision, resource: resource}
	}
	if err != nil || o.draining {
		if o.candidate != nil {
			o.startCloseLocked(o.candidate)
		}
		o.signalLocked()
		if err != nil {
			return err
		}
		return errors.New("resources are draining")
	}
	o.signalLocked()
	return nil
}

// All owner failure checks precede apply. The caller's serialized apply must
// retain its old snapshot on error; after nil the final resource swap cannot fail.
func (o *websocketResources) publish(revision string, apply func() error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.draining {
		return errors.New("resources are draining")
	}
	if o.current != nil && o.current.revision == revision {
		return nil
	}
	next := o.candidate
	if o.preparing || next == nil || next.revision != revision || next.closing || next.closeErr != nil || o.retired[revision] != nil {
		return errors.New("resource candidate not ready")
	}
	if o.current != nil && len(o.retired) >= 2 {
		return errors.New("retiring resource capacity reached")
	}
	if apply != nil {
		if err := apply(); err != nil {
			return err
		}
	}
	old := o.current
	o.current = next
	o.candidate = nil
	if old != nil {
		o.retired[old.revision] = old
		if old.refs == 0 {
			o.startCloseLocked(old)
		}
	}
	o.signalLocked()
	return nil
}

type websocketResourceLease struct {
	owner      *websocketResources
	generation *websocketGeneration
	once       sync.Once
}

func (l *websocketResourceLease) Revision() string { return l.generation.revision }
func (l *websocketResourceLease) Release() error {
	l.once.Do(func() {
		o := l.owner
		o.mu.Lock()
		defer o.mu.Unlock()
		g := l.generation
		g.refs--
		if g.refs == 0 && o.retired[g.revision] == g {
			o.startCloseLocked(g)
		}
		o.signalLocked()
	})
	return nil
}
func (o *websocketResources) acquire() (v2.Lease, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.draining || o.current == nil {
		return nil, errors.New("no running websocket resources")
	}
	o.current.refs++
	return &websocketResourceLease{owner: o, generation: o.current}, nil
}
func (o *websocketResources) discard() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.candidate != nil {
		o.startCloseLocked(o.candidate)
	}
}
func (o *websocketResources) startCloseLocked(g *websocketGeneration) {
	if g.closing || g.refs != 0 {
		return
	}
	g.closing = true
	g.closeErr = nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
		defer cancel()
		err := g.resource.Close(ctx)
		o.mu.Lock()
		defer o.mu.Unlock()
		g.closing = false
		g.closeErr = err
		if err == nil {
			if o.candidate == g {
				o.candidate = nil
			}
			if o.retired[g.revision] == g {
				delete(o.retired, g.revision)
			}
		}
		o.signalLocked()
	}()
}
func (o *websocketResources) retryClose() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.candidate != nil && o.candidate.closeErr != nil {
		o.startCloseLocked(o.candidate)
	}
	for _, g := range o.retired {
		if g.closeErr != nil {
			o.startCloseLocked(g)
		}
	}
}
func (o *websocketResources) drain(ctx context.Context) error {
	o.mu.Lock()
	o.draining = true
	if o.current != nil {
		g := o.current
		o.current = nil
		o.retired[g.revision] = g
		if g.refs == 0 {
			o.startCloseLocked(g)
		}
	}
	if o.candidate != nil {
		o.startCloseLocked(o.candidate)
	}
	for _, g := range o.retired {
		if g.refs == 0 {
			o.startCloseLocked(g)
		}
	}
	o.signalLocked()
	for {
		if !o.preparing && o.candidate == nil && len(o.retired) == 0 {
			o.mu.Unlock()
			return nil
		}
		for _, g := range o.retired {
			if g.closeErr != nil && !g.closing {
				o.mu.Unlock()
				return errors.New("websocket resource close failed; owner retained")
			}
		}
		if o.candidate != nil && o.candidate.closeErr != nil && !o.candidate.closing {
			o.mu.Unlock()
			return errors.New("websocket candidate close failed; owner retained")
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		o.mu.Lock()
	}
}
