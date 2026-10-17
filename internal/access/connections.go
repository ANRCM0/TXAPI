package access

import (
	"context"
	"fmt"
	"sync"
)

// ConnectionCapacity is independent of generation/attempt leases and rate
// admission. Binding must come from the authenticated connection boundary.
type ConnectionCapacity struct {
	mu          sync.Mutex
	config      ConnectionLimits
	closed      bool
	active      int
	users, keys map[uint]int
}

type ConnectionLimits struct{ Instance, User, Key int }

type ConnectionSnapshot struct{ Active, Users, Keys int }

func NewConnectionCapacity(limits ConnectionLimits) (*ConnectionCapacity, error) {
	if limits.Instance < 2 || limits.Instance > 4096 || limits.User < 1 || limits.User >= limits.Instance || limits.Key < 1 || limits.Key > limits.User {
		return nil, fmt.Errorf("connection limits require 1 <= key <= user < instance <= 4096")
	}
	return &ConnectionCapacity{config: limits, users: make(map[uint]int), keys: make(map[uint]int)}, nil
}

func (c *ConnectionCapacity) Acquire(ctx context.Context, binding Binding) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if binding.UserID == 0 || binding.TokenID == 0 {
		return nil, ErrUnauthorized
	}
	if c.closed {
		return nil, &CapacityError{Dimension: "closed connections", Unavailable: true}
	}
	if c.users[binding.UserID] >= c.config.User {
		return nil, &CapacityError{Dimension: "user connections", RetryAfterSeconds: 1}
	}
	if c.keys[binding.TokenID] >= c.config.Key {
		return nil, &CapacityError{Dimension: "key connections", RetryAfterSeconds: 1}
	}
	if c.active >= c.config.Instance {
		return nil, &CapacityError{Dimension: "instance connections", RetryAfterSeconds: 1, Unavailable: true}
	}
	c.active++
	c.users[binding.UserID]++
	c.keys[binding.TokenID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.active--
			decrement(c.users, binding.UserID)
			decrement(c.keys, binding.TokenID)
		})
	}, nil
}

// Close stops new connections without forgetting leases still owned by peers.
func (c *ConnectionCapacity) Close() { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true }

func (c *ConnectionCapacity) Snapshot() ConnectionSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ConnectionSnapshot{Active: c.active, Users: len(c.users), Keys: len(c.keys)}
}
