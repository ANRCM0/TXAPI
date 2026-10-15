package access

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CapacityConfig contains process-wide ceilings. A user's or Key's nonzero
// setting can lower these ceilings; zero inherits them. User < Instance leaves
// capacity for other users even when one user owns many Keys.
type CapacityConfig struct {
	Instance int
	User     int
	Key      int
	Channel  int
}

func (c CapacityConfig) Validate() error {
	for _, n := range []int{c.Instance, c.User, c.Key, c.Channel} {
		if n < 1 || n > 1_000_000 {
			return fmt.Errorf("concurrency ceilings must be between 1 and 1000000")
		}
	}
	if c.User >= c.Instance || c.Key > c.User {
		return fmt.Errorf("concurrency ceilings require key <= user < instance")
	}
	return nil
}

// CapacityError is an admission decision, never an upstream health failure.
type CapacityError struct {
	Dimension         string
	RetryAfterSeconds int
	Unavailable       bool
}

func (e *CapacityError) Error() string { return e.Dimension + " capacity reached" }

const maxRateUsers = 4096
const maxRateEvents = 65536

type rateWindow struct {
	events []time.Time
	head   int
}

func (w *rateWindow) size() int {
	if w == nil {
		return 0
	}
	return len(w.events) - w.head
}

// Capacity owns atomic request reservations and nested attempt reservations.
// There is no waiting queue or background goroutine. Only active concurrency
// entries are retained; RPM state expires after one minute and has hard bounds.
type Capacity struct {
	mu         sync.Mutex
	config     CapacityConfig
	active     int
	users      map[uint]int
	keys       map[uint]int
	channels   map[uint]int
	rates      map[uint]*rateWindow
	rateEvents int
	nextSweep  time.Time
	now        func() time.Time
	closed     bool
}

func NewCapacity(config CapacityConfig) (*Capacity, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Capacity{config: config, users: make(map[uint]int), keys: make(map[uint]int), channels: make(map[uint]int), rates: make(map[uint]*rateWindow), now: time.Now}, nil
}

// AcquireRequest atomically admits the instance, user and Key together. The
// user RPM bucket is shared by all Keys and charged once per logical request.
func (c *Capacity) AcquireRequest(ctx context.Context, p Principal) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed {
		return nil, &CapacityError{Dimension: "closed", Unavailable: true}
	}
	if p.UserID == 0 || p.TokenID == 0 || p.UserConcurrencyLimit < 0 || p.KeyConcurrencyLimit < 0 || p.UserRPMLimit < 0 {
		return nil, fmt.Errorf("invalid verified principal for capacity admission")
	}
	if c.users[p.UserID] >= ceiling(c.config.User, p.UserConcurrencyLimit) {
		return nil, &CapacityError{Dimension: "user concurrency", RetryAfterSeconds: 1}
	}
	if c.keys[p.TokenID] >= ceiling(c.config.Key, p.KeyConcurrencyLimit) {
		return nil, &CapacityError{Dimension: "key concurrency", RetryAfterSeconds: 1}
	}
	if c.active >= c.config.Instance {
		return nil, &CapacityError{Dimension: "instance concurrency", RetryAfterSeconds: 1, Unavailable: true}
	}
	if p.UserRPMLimit > 0 {
		now := c.now()
		c.sweepRates(now)
		bucket := c.pruneRate(p.UserID, now)
		if bucket.size() >= p.UserRPMLimit {
			retry := int(bucket.events[bucket.head].Add(time.Minute).Sub(now).Seconds()) + 1
			return nil, &CapacityError{Dimension: "user request rate", RetryAfterSeconds: retry}
		}
		// Never evict a live rate bucket to admit a new identity: eviction
		// would silently reset its window and bypass the configured limit.
		if (bucket == nil && len(c.rates) >= maxRateUsers) || c.rateEvents >= maxRateEvents {
			return nil, &CapacityError{Dimension: "rate tracker", RetryAfterSeconds: 1, Unavailable: true}
		}
		if bucket == nil {
			bucket = &rateWindow{}
			c.rates[p.UserID] = bucket
		}
		bucket.events = append(bucket.events, now)
		c.rateEvents++
	}
	c.active++
	c.users[p.UserID]++
	c.keys[p.TokenID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.active--
			decrement(c.users, p.UserID)
			decrement(c.keys, p.TokenID)
		})
	}, nil
}

// AcquireAttempt holds only channel capacity. A request retains its user/Key
// reservation while retrying, but releases the old channel before a new attempt.
func (c *Capacity) AcquireAttempt(ctx context.Context, channelID uint) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed {
		return nil, &CapacityError{Dimension: "closed", Unavailable: true}
	}
	if channelID == 0 {
		return nil, fmt.Errorf("invalid channel for capacity admission")
	}
	if c.channels[channelID] >= c.config.Channel {
		return nil, &CapacityError{Dimension: "channel concurrency", RetryAfterSeconds: 1}
	}
	c.channels[channelID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			decrement(c.channels, channelID)
		})
	}, nil
}
func ceiling(server, individual int) int {
	if individual > 0 && individual < server {
		return individual
	}
	return server
}
func decrement(counts map[uint]int, id uint) {
	if counts[id] <= 1 {
		delete(counts, id)
	} else {
		counts[id]--
	}
}
func (c *Capacity) pruneRate(id uint, now time.Time) *rateWindow {
	bucket := c.rates[id]
	if bucket == nil {
		return nil
	}
	before := bucket.head
	cutoff := now.Add(-time.Minute)
	for bucket.head < len(bucket.events) && !bucket.events[bucket.head].After(cutoff) {
		bucket.head++
	}
	c.rateEvents -= bucket.head - before
	if bucket.head == len(bucket.events) {
		delete(c.rates, id)
		return nil
	}
	if bucket.head >= bucket.size() {
		// Compact only after at least half expires. Copying on every expired
		// event would make a busy rolling window quadratic. Between copies,
		// retained expired slots stay below the number of live events.
		bucket.events = append([]time.Time(nil), bucket.events[bucket.head:]...)
		bucket.head = 0
	}
	return bucket
}
func (c *Capacity) sweepRates(now time.Time) {
	if now.Before(c.nextSweep) {
		return
	}
	for id := range c.rates {
		c.pruneRate(id, now)
	}
	c.nextSweep = now.Add(time.Second)
}

// Close rejects new acquisitions, retaining active leases until their owners
// release them. It does not drop occupied counters during graceful shutdown.
func (c *Capacity) Close() { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true }

type CapacitySnapshot struct {
	Requests   int
	Attempts   int
	Users      int
	Keys       int
	Channels   int
	RateUsers  int
	RateEvents int
}

func (c *Capacity) Snapshot() CapacitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepRates(c.now())
	s := CapacitySnapshot{Requests: c.active, Users: len(c.users), Keys: len(c.keys), Channels: len(c.channels), RateUsers: len(c.rates), RateEvents: c.rateEvents}
	for _, count := range c.channels {
		s.Attempts += count
	}
	return s
}
