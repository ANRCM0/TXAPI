package gateway

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	shadowSlotCount            = 8
	shadowOutcomeCount         = 64
	shadowRetention            = 10 * time.Minute
	shadowSlotActiveCap uint64 = 65535
	shadowAggregateCap  uint64 = 1048576
)

type shadowOutcome uint8

const (
	shadowIgnored shadowOutcome = iota
	shadowCleanHTTP200
	shadowHTTPError
)

const (
	shadowConsumed uint8 = iota
	shadowTracked
	shadowUntracked
	shadowRetired
)

type shadowCandidate struct {
	channelID uint
	epoch     uint64
}

type shadowToken struct {
	epoch, slotGeneration uint64
	slot                  int
	kind                  uint8
}

type shadowCandidateSnapshot struct {
	channelID              uint
	active                 uint64
	live, errors           int
	neutral                bool
	numerator, denominator uint64
}

type shadowComparison struct {
	reason            string
	recommended       uint
	compared, matched bool
	epoch             uint64
	count             int
	candidates        [shadowSlotCount]shadowCandidateSnapshot
}

type shadowSample struct {
	at      time.Time
	outcome shadowOutcome
}

type shadowSlot struct {
	channelID  uint
	generation uint64
	active     uint64
	lastUsed   time.Time
	next       int
	samples    [shadowOutcomeCount]shadowSample
}

type shadowSampler struct {
	mu                             sync.Mutex
	appliedEpoch                   *atomic.Uint64
	epoch, generation              uint64
	retiredActive, untrackedActive uint64
	closed, disabled               bool
	slots                          [shadowSlotCount]shadowSlot
}

func newShadowSampler(appliedEpoch *atomic.Uint64) *shadowSampler {
	return &shadowSampler{appliedEpoch: appliedEpoch}
}

// advance never takes the registry lock. Old tokens release one bounded
// aggregate after publication, rather than retaining a table for every epoch.
func (s *shadowSampler) advance() {
	if s.closed || s.disabled || s.appliedEpoch == nil {
		return
	}
	epoch := s.appliedEpoch.Load()
	if epoch == 0 || epoch < s.epoch {
		s.disabled = true
		return
	}
	if epoch == s.epoch {
		return
	}
	active := s.retiredActive
	if !s.addActive(&active, s.untrackedActive) {
		return
	}
	for i := range s.slots {
		if !s.addActive(&active, s.slots[i].active) {
			return
		}
	}
	s.retiredActive, s.untrackedActive, s.epoch = active, 0, epoch
	s.slots = [shadowSlotCount]shadowSlot{}
}

func (s *shadowSampler) addActive(dst *uint64, n uint64) bool {
	if *dst >= shadowAggregateCap || n >= shadowAggregateCap-*dst {
		s.disabled = true
		return false
	}
	*dst += n
	return true
}

func (s *shadowSampler) usable() bool {
	return !s.closed && !s.disabled && s.appliedEpoch != nil
}

func (s *shadowSampler) Begin(snapshotEpoch uint64, channelID uint, now time.Time) shadowToken {
	if s == nil {
		return shadowToken{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	if !s.usable() {
		return shadowToken{}
	}
	if snapshotEpoch != s.epoch || channelID == 0 {
		if !s.addActive(&s.retiredActive, 1) {
			return shadowToken{}
		}
		return shadowToken{epoch: snapshotEpoch, kind: shadowRetired}
	}
	i := s.slot(channelID, [shadowSlotCount]shadowCandidate{}, 0)
	if i < 0 {
		if !s.addActive(&s.untrackedActive, 1) {
			return shadowToken{}
		}
		return shadowToken{epoch: snapshotEpoch, kind: shadowUntracked}
	}
	if s.slots[i].active >= shadowSlotActiveCap {
		s.disabled = true
		return shadowToken{}
	}
	s.slots[i].active++
	s.slots[i].lastUsed = now
	return shadowToken{epoch: snapshotEpoch, slotGeneration: s.slots[i].generation, slot: i, kind: shadowTracked}
}

// End consumes the caller-owned token even when the sampler has closed. A
// repeated End on the same pointer cannot decrement a replacement slot.
func (s *shadowSampler) End(token *shadowToken, outcome shadowOutcome, now time.Time) bool {
	if token == nil {
		return false
	}
	if s == nil {
		*token = shadowToken{}
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := *token
	*token = shadowToken{}
	s.advance()
	if !s.usable() || t.kind == shadowConsumed {
		return false
	}
	if t.kind == shadowRetired || t.epoch != s.epoch {
		if s.retiredActive == 0 {
			s.disabled = true
		} else {
			s.retiredActive--
		}
		return false
	}
	if t.kind == shadowUntracked {
		if s.untrackedActive == 0 {
			s.disabled = true
		} else {
			s.untrackedActive--
		}
		return false
	}
	if t.kind != shadowTracked || t.slot < 0 || t.slot >= shadowSlotCount {
		s.disabled = true
		return false
	}
	slot := &s.slots[t.slot]
	if slot.generation != t.slotGeneration || slot.active == 0 {
		s.disabled = true
		return false
	}
	slot.active--
	slot.lastUsed = now
	if outcome != shadowCleanHTTP200 && outcome != shadowHTTPError {
		return false
	}
	slot.samples[slot.next] = shadowSample{at: now, outcome: outcome}
	slot.next = (slot.next + 1) % shadowOutcomeCount
	return true
}

func (s *shadowSampler) slot(channelID uint, pool [shadowSlotCount]shadowCandidate, count int) int {
	for i := range s.slots {
		if s.slots[i].channelID == channelID {
			return i
		}
	}
	chosen := -1
	for i := range s.slots {
		slot := &s.slots[i]
		if slot.active != 0 {
			continue
		}
		protected := false
		for j := 0; j < count; j++ {
			if slot.channelID == pool[j].channelID {
				protected = true
				break
			}
		}
		if protected {
			continue
		}
		if slot.channelID == 0 {
			chosen = i
			break
		}
		if chosen < 0 || slot.lastUsed.Before(s.slots[chosen].lastUsed) {
			chosen = i
		}
	}
	if chosen < 0 {
		return -1
	}
	if s.generation == ^uint64(0) {
		s.disabled = true
		return -1
	}
	s.generation++
	s.slots[chosen] = shadowSlot{channelID: channelID, generation: s.generation}
	return chosen
}

func (s *shadowSampler) Compare(snapshotEpoch uint64, pool [shadowSlotCount]shadowCandidate, count int, pinned bool, actual uint, now time.Time) shadowComparison {
	out := shadowComparison{reason: "disabled", count: count}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	out.epoch = s.epoch
	if s.closed || s.appliedEpoch == nil {
		return out
	}
	// Eligibility reasons precede accounting state, keeping metrics denominators
	// independent of an unrelated saturation or publication race.
	if pinned {
		out.reason = "pinned"
		return out
	}
	if count < 2 || count > shadowSlotCount {
		out.reason = "pool_size"
		return out
	}
	if s.disabled {
		out.reason = "saturated"
		return out
	}
	if snapshotEpoch != s.epoch {
		out.reason = "stale_epoch"
		return out
	}
	for i := 0; i < count; i++ {
		if pool[i].channelID == 0 || pool[i].epoch != s.epoch {
			out.reason = "stale_epoch"
			return out
		}
		for j := 0; j < i; j++ {
			if pool[j].channelID == pool[i].channelID {
				out.reason = "stale_epoch"
				return out
			}
		}
	}
	if s.retiredActive != 0 {
		out.reason = "retired_active"
		return out
	}
	if s.untrackedActive != 0 {
		out.reason = "untracked_active"
		return out
	}
	best := -1
	for i := 0; i < count; i++ {
		index := s.slot(pool[i].channelID, pool, count)
		if index < 0 {
			out.reason = "slots_busy"
			if s.disabled {
				out.reason = "saturated"
			}
			return out
		}
		slot := &s.slots[index]
		slot.lastUsed = now
		snapshot := shadowCandidateSnapshot{channelID: slot.channelID, active: slot.active}
		for j := range slot.samples {
			sample := &slot.samples[j]
			if sample.outcome == shadowIgnored {
				continue
			}
			if sample.at.After(now) || now.Sub(sample.at) >= shadowRetention {
				*sample = shadowSample{}
				continue
			}
			snapshot.live++
			if sample.outcome == shadowHTTPError {
				snapshot.errors++
			}
		}
		snapshot.neutral = snapshot.live < 4
		if snapshot.neutral {
			snapshot.numerator, snapshot.denominator = 3*(snapshot.active+1), 1
		} else {
			snapshot.denominator = uint64(snapshot.live + 2)
			snapshot.numerator = (snapshot.active + 1) * (snapshot.denominator + 4*uint64(snapshot.errors+1))
		}
		out.candidates[i] = snapshot
		if best < 0 || shadowLess(snapshot, out.candidates[best]) {
			best = i
		}
	}
	out.recommended, out.compared = out.candidates[best].channelID, true
	out.matched = out.recommended == actual
	out.reason = "compared_different"
	if out.matched {
		out.reason = "compared_equal"
	}
	return out
}

func shadowLess(a, b shadowCandidateSnapshot) bool {
	left, right := a.numerator*b.denominator, b.numerator*a.denominator
	return left < right || left == right && a.channelID < b.channelID
}

func (s *shadowSampler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.slots = [shadowSlotCount]shadowSlot{}
		s.retiredActive, s.untrackedActive = 0, 0
	}
}
