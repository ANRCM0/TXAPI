package gateway

import (
	"errors"
	"sync"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

var errRuntimeRevision = errors.New("runtime revision is unavailable")

type runtimeGeneration struct {
	revision string
	refs     int
	retiring bool
}

type RuntimeLeaseSet struct {
	mu          sync.Mutex
	current     *runtimeGeneration
	retiring    map[string]*runtimeGeneration
	maxRetiring int
}

type RuntimeLease struct {
	owner      *RuntimeLeaseSet
	generation *runtimeGeneration
	once       sync.Once
}

var _ v2.Lease = (*RuntimeLease)(nil)

func NewRuntimeLeaseSet(initial string, maxRetiring int) (*RuntimeLeaseSet, error) {
	if initial == "" || maxRetiring < 0 {
		return nil, errRuntimeRevision
	}
	return &RuntimeLeaseSet{current: &runtimeGeneration{revision: initial}, retiring: make(map[string]*runtimeGeneration), maxRetiring: maxRetiring}, nil
}

func (s *RuntimeLeaseSet) Publish(revision string) error {
	if s == nil || revision == "" {
		return errRuntimeRevision
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.revision == revision {
		return nil
	}
	if _, exists := s.retiring[revision]; exists {
		return errRuntimeRevision
	}
	if len(s.retiring) >= s.maxRetiring && s.current != nil && s.current.refs > 0 {
		return errRuntimeRevision
	}
	if s.current != nil {
		s.current.retiring = true
		if s.current.refs == 0 {
			// The generation has no resource owner left; discard it immediately.
		} else {
			s.retiring[s.current.revision] = s.current
		}
	}
	s.current = &runtimeGeneration{revision: revision}
	return nil
}

func (s *RuntimeLeaseSet) Acquire() (*RuntimeLease, error) {
	if s == nil {
		return nil, errRuntimeRevision
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, errRuntimeRevision
	}
	s.current.refs++
	return &RuntimeLease{owner: s, generation: s.current}, nil
}

func (s *RuntimeLeaseSet) Current() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return ""
	}
	return s.current.revision
}

func (l *RuntimeLease) Revision() string {
	if l == nil || l.generation == nil {
		return ""
	}
	return l.generation.revision
}

func (l *RuntimeLease) Release() error {
	if l == nil || l.owner == nil || l.generation == nil {
		return errRuntimeRevision
	}
	l.once.Do(func() {
		l.owner.mu.Lock()
		if l.generation.refs > 0 {
			l.generation.refs--
		}
		if l.generation.retiring && l.generation.refs == 0 {
			delete(l.owner.retiring, l.generation.revision)
		}
		l.owner.mu.Unlock()
	})
	return nil
}
