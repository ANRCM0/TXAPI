package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func samplerFixture() (*shadowSampler, *atomic.Uint64, time.Time) {
	epoch := &atomic.Uint64{}
	epoch.Store(1)
	return newShadowSampler(epoch), epoch, time.Unix(1700000000, 0)
}

func samplerPool(epoch uint64, ids ...uint) (pool [shadowSlotCount]shadowCandidate) {
	for i, id := range ids {
		pool[i] = shadowCandidate{channelID: id, epoch: epoch}
	}
	return
}

func samplerRecord(t *testing.T, s *shadowSampler, epoch uint64, id uint, outcome shadowOutcome, now time.Time) {
	t.Helper()
	token := s.Begin(epoch, id, now)
	if appended := s.End(&token, outcome, now); appended != (outcome == shadowCleanHTTP200 || outcome == shadowHTTPError) {
		t.Fatal("unexpected sample append", appended, outcome)
	}
}

func TestShadowSamplerNeutralThresholdExactScoresAndLoad(t *testing.T) {
	s, _, now := samplerFixture()
	pool := samplerPool(1, 2, 1)
	for i := 0; i < 3; i++ {
		samplerRecord(t, s, 1, 2, shadowCleanHTTP200, now)
	}
	out := s.Compare(1, pool, 2, false, 2, now)
	if !out.compared || out.recommended != 1 || out.matched || out.reason != "compared_different" || !out.candidates[0].neutral || out.candidates[0].numerator != 3 || out.candidates[0].denominator != 1 {
		t.Fatal("unknown must remain neutral, with deterministic ID tie", out)
	}
	samplerRecord(t, s, 1, 2, shadowCleanHTTP200, now)
	out = s.Compare(1, pool, 2, false, 2, now)
	if !out.matched || out.reason != "compared_equal" || out.candidates[0].neutral || out.candidates[0].numerator != 10 || out.candidates[0].denominator != 6 || out.candidates[0].live != 4 || out.candidates[0].errors != 0 {
		t.Fatal("four samples must enable exact smoothing", out)
	}
	busy := s.Begin(1, 2, now)
	out = s.Compare(1, pool, 2, false, 1, now)
	if out.recommended != 1 || out.candidates[0].active != 1 || out.candidates[0].numerator != 20 {
		t.Fatal("active host attempt must affect the comparison", out)
	}
	if s.End(&busy, shadowIgnored, now) {
		t.Fatal("ignored completion appended")
	}
	for i := 0; i < 4; i++ {
		samplerRecord(t, s, 1, 1, shadowHTTPError, now)
	}
	out = s.Compare(1, pool, 2, false, 2, now)
	if out.recommended != 2 || out.candidates[1].numerator != 26 || out.candidates[1].denominator != 6 || out.candidates[1].errors != 4 {
		t.Fatal("explicit HTTP errors must affect rational score", out)
	}
}

func TestShadowSamplerRingBoundExpiryAndFutureClock(t *testing.T) {
	s, _, now := samplerFixture()
	for i := 0; i < 100; i++ {
		samplerRecord(t, s, 1, 1, shadowHTTPError, now)
	}
	pool := samplerPool(1, 1, 2)
	out := s.Compare(1, pool, 2, false, 2, now.Add(shadowRetention-time.Nanosecond))
	if out.candidates[0].live != 64 || out.candidates[0].errors != 64 || out.candidates[0].neutral {
		t.Fatal("ring must retain only 64 live outcomes", out)
	}
	out = s.Compare(1, pool, 2, false, 1, now.Add(shadowRetention))
	if out.candidates[0].live != 0 || !out.candidates[0].neutral || !out.matched {
		t.Fatal("expiry boundary must return to neutral", out)
	}
	samplerRecord(t, s, 1, 1, shadowCleanHTTP200, now.Add(time.Hour))
	out = s.Compare(1, pool, 2, false, 1, now)
	if out.candidates[0].live != 0 {
		t.Fatal("future timestamps cannot manufacture live history", out)
	}
}

func TestShadowSamplerIgnoredAndRepeatedEnd(t *testing.T) {
	s, _, now := samplerFixture()
	token := s.Begin(1, 1, now)
	if s.End(&token, shadowIgnored, now) || s.End(&token, shadowHTTPError, now) || token != (shadowToken{}) {
		t.Fatal("token must be consumed once", token)
	}
	out := s.Compare(1, samplerPool(1, 1, 2), 2, false, 1, now)
	if !out.compared || out.candidates[0].active != 0 || out.candidates[0].live != 0 || s.disabled {
		t.Fatal("ignored/repeated completion changed accounting", out)
	}
	if s.End(nil, shadowHTTPError, now) {
		t.Fatal("nil token appended")
	}
}

func TestShadowSamplerBusySlotsUntrackedAndProtectedPool(t *testing.T) {
	s, _, now := samplerFixture()
	var tokens [shadowSlotCount]shadowToken
	for i := range tokens {
		tokens[i] = s.Begin(1, uint(i+1), now)
	}
	pool := samplerPool(1, 1, 9)
	if out := s.Compare(1, pool, 2, false, 1, now); out.reason != "slots_busy" {
		t.Fatal("busy slots cannot be evicted", out)
	}
	untracked := s.Begin(1, 9, now)
	if untracked.kind != shadowUntracked || s.untrackedActive != 1 {
		t.Fatal("ninth active channel must remain explicitly unknown", untracked, s.untrackedActive)
	}
	if out := s.Compare(1, samplerPool(1, 1, 2), 2, false, 1, now); out.reason != "untracked_active" {
		t.Fatal("unknown activity must not become zero", out)
	}
	if s.End(&untracked, shadowHTTPError, now) || s.untrackedActive != 0 {
		t.Fatal("untracked outcome must not create a slot")
	}
	for i := range tokens {
		s.End(&tokens[i], shadowIgnored, now.Add(time.Duration(i)*time.Second))
	}
	// Channel 1 is oldest, but belongs to this pool and must survive reserving 9.
	out := s.Compare(1, samplerPool(1, 9, 1), 2, false, 1, now.Add(time.Minute))
	if !out.compared || out.recommended != 1 || s.disabled {
		t.Fatal("whole candidate pool must be protected during eviction", out)
	}
	var found1, found2, found9 bool
	for _, slot := range s.slots {
		found1 = found1 || slot.channelID == 1
		found2 = found2 || slot.channelID == 2
		found9 = found9 || slot.channelID == 9
	}
	if !found1 || found2 || !found9 {
		t.Fatal("expected inactive non-pool LRU eviction", found1, found2, found9)
	}
}

func TestShadowSamplerPublicationRetiresAllActivityUntilFinalExit(t *testing.T) {
	s, epoch, now := samplerFixture()
	old := s.Begin(1, 1, now)
	samplerRecord(t, s, 1, 2, shadowHTTPError, now)
	epoch.Store(2)
	current := s.Begin(2, 1, now)
	stale := s.Begin(1, 2, now)
	if out := s.Compare(2, samplerPool(2, 1, 2), 2, false, 1, now); out.reason != "retired_active" || s.retiredActive != 2 {
		t.Fatal("old same-channel activity cannot be replaced with zero", out, s.retiredActive)
	}
	epoch.Store(3)
	if s.End(&old, shadowHTTPError, now) || s.retiredActive != 2 {
		t.Fatal("old End must first advance and carry current activity", s.retiredActive)
	}
	if s.End(&current, shadowCleanHTTP200, now) || s.retiredActive != 1 {
		t.Fatal("second old epoch must release only aggregate", s.retiredActive)
	}
	if out := s.Compare(3, samplerPool(3, 1, 2), 2, false, 1, now); out.reason != "retired_active" {
		t.Fatal("stale Begin still occupies retired coverage", out)
	}
	if s.End(&stale, shadowHTTPError, now) || s.retiredActive != 0 {
		t.Fatal("stale Begin token failed to release")
	}
	out := s.Compare(3, samplerPool(3, 1, 2), 2, false, 1, now)
	if !out.compared || out.candidates[0].active != 0 || out.candidates[0].live != 0 || out.candidates[1].live != 0 || s.disabled {
		t.Fatal("retired outcomes must not contaminate new epoch", out)
	}
}

func TestShadowSamplerOldEndFirstAdvancesUntrackedWithoutPerEpochState(t *testing.T) {
	s, epoch, now := samplerFixture()
	var tokens [9]shadowToken
	for i := range tokens {
		tokens[i] = s.Begin(1, uint(i+1), now)
	}
	epoch.Store(2)
	if s.End(&tokens[8], shadowHTTPError, now) || s.retiredActive != 8 || s.untrackedActive != 0 || s.epoch != 2 {
		t.Fatal("untracked End first must release retired aggregate", s.retiredActive, s.untrackedActive, s.epoch)
	}
	for i := 0; i < 8; i++ {
		epoch.Store(uint64(i + 3))
		s.End(&tokens[i], shadowIgnored, now)
	}
	if s.retiredActive != 0 || s.disabled {
		t.Fatal("multiple publications leaked activity", s.retiredActive)
	}
}

func TestShadowSamplerEpochUnchangedPreservesSamplesAndRejectsMixedPool(t *testing.T) {
	s, _, now := samplerFixture()
	for i := 0; i < 4; i++ {
		samplerRecord(t, s, 1, 1, shadowCleanHTTP200, now)
	}
	pool := samplerPool(1, 1, 2)
	first := s.Compare(1, pool, 2, false, 1, now)
	second := s.Compare(1, pool, 2, false, 1, now)
	if first != second || second.candidates[0].live != 4 {
		t.Fatal("same applied epoch must preserve history", first, second)
	}
	pool[1].epoch = 2
	if out := s.Compare(1, pool, 2, false, 1, now); out.reason != "stale_epoch" || s.epoch != 1 {
		t.Fatal("mixed pool cannot advance sampler backwards/forwards", out)
	}
	pool = samplerPool(1, 1, 1)
	if out := s.Compare(1, pool, 2, false, 1, now); out.reason != "stale_epoch" {
		t.Fatal("duplicate targets must not create biased comparison", out)
	}
}

func TestShadowSamplerSaturationLatchesAcrossPublicationAndEligibilityPriority(t *testing.T) {
	for _, scenario := range []string{"slot", "untracked", "retired", "generation", "epoch_zero", "epoch_backwards"} {
		t.Run(scenario, func(t *testing.T) {
			s, epoch, now := samplerFixture()
			token := s.Begin(1, 1, now)
			s.End(&token, shadowIgnored, now)
			switch scenario {
			case "slot":
				s.slots[0].active = shadowSlotActiveCap
				s.Begin(1, 1, now)
			case "untracked":
				for i := range s.slots {
					s.slots[i].channelID = uint(i + 1)
					s.slots[i].active = 1
				}
				s.untrackedActive = shadowAggregateCap - 1
				s.Begin(1, 9, now)
			case "retired":
				s.retiredActive = shadowAggregateCap - 1
				s.slots[0].active = 1
				epoch.Store(2)
			case "generation":
				s.generation = ^uint64(0)
				s.Begin(1, 2, now)
			case "epoch_zero":
				epoch.Store(0)
			case "epoch_backwards":
				epoch.Store(2)
				s.Compare(2, samplerPool(2, 1, 2), 2, false, 1, now)
				epoch.Store(1)
			}
			if out := s.Compare(epoch.Load(), samplerPool(epoch.Load(), 1, 2), 2, false, 1, now); out.reason != "saturated" {
				t.Fatal("accounting limit must fail diagnostic closed", out)
			}
			if out := s.Compare(1, samplerPool(1, 1, 2), 2, true, 1, now); out.reason != "pinned" {
				t.Fatal("pinned is excluded from eligible metrics even after saturation", out)
			}
			if out := s.Compare(1, samplerPool(1, 1), 1, false, 1, now); out.reason != "pool_size" {
				t.Fatal("pool size is excluded before accounting state", out)
			}
			epoch.Store(100)
			if out := s.Compare(100, samplerPool(100, 1, 2), 2, false, 1, now); out.reason != "saturated" {
				t.Fatal("publication cannot restore saturated feature", out)
			}
		})
	}
}

func TestShadowSamplerCloseDropsStateAndLateTokens(t *testing.T) {
	s, epoch, now := samplerFixture()
	token := s.Begin(1, 1, now)
	s.Close()
	s.Close()
	epoch.Store(2)
	if s.End(&token, shadowHTTPError, now) || s.Begin(2, 1, now) != (shadowToken{}) {
		t.Fatal("closed sampler accepted observation")
	}
	if out := s.Compare(2, samplerPool(2, 1, 2), 2, true, 1, now); out.reason != "disabled" || s.slots != ([shadowSlotCount]shadowSlot{}) {
		t.Fatal("close must drop fixed storage and exclude eligibility", out)
	}
	var nilSampler *shadowSampler
	if out := nilSampler.Compare(1, samplerPool(1, 1, 2), 2, false, 1, now); out.reason != "disabled" {
		t.Fatal(out)
	}
}

func TestShadowSamplerConcurrentCompletionsAndPublicationsStayBounded(t *testing.T) {
	s, epoch, now := samplerFixture()
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				e := epoch.Load()
				token := s.Begin(e, uint(worker%8+1), now)
				s.Compare(e, samplerPool(e, 1, 2, 3, 4, 5, 6, 7, 8), 8, false, 1, now)
				s.End(&token, shadowCleanHTTP200, now)
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for e := uint64(2); e <= 100; e++ {
			epoch.Store(e)
		}
	}()
	wg.Wait()
	out := s.Compare(epoch.Load(), samplerPool(epoch.Load(), 1, 2, 3, 4, 5, 6, 7, 8), 8, false, 1, now)
	if !out.compared || s.retiredActive != 0 || s.untrackedActive != 0 || s.disabled {
		t.Fatal("concurrent lifecycle leaked or saturated", out, s.retiredActive, s.untrackedActive)
	}
	for _, candidate := range out.candidates {
		if candidate.active != 0 || candidate.live > shadowOutcomeCount {
			t.Fatal("fixed bounds broken", candidate)
		}
	}
}
