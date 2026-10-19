package gateway

import (
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

func TestShadowSamplerObservationDoesNotAllocate(t *testing.T) {
	var epoch atomic.Uint64
	epoch.Store(1)
	sampler := newShadowSampler(&epoch)
	defer sampler.Close()
	var pool [8]shadowCandidate
	for i := range pool {
		pool[i] = shadowCandidate{channelID: uint(i + 1), epoch: 1}
	}
	now := time.Now()
	if comparison := sampler.Compare(1, pool, len(pool), false, 1, now); !comparison.compared {
		t.Fatal("allocation fixture did not warm all eight slots", comparison)
	}
	for _, candidate := range pool {
		for i := 0; i < shadowOutcomeCount; i++ {
			token := sampler.Begin(1, candidate.channelID, now)
			sampler.End(&token, shadowCleanHTTP200, now)
		}
	}
	allocations := testing.AllocsPerRun(1000, func() {
		token := sampler.Begin(1, 1, now)
		sampler.End(&token, shadowCleanHTTP200, now)
		sampler.Compare(1, pool, len(pool), false, 1, now)
	})
	if allocations != 0 {
		t.Fatalf("bounded observation allocates: %g allocations per begin/end/comparison", allocations)
	}
}

func warmShadowBenchmark(g *Gateway, now time.Time) {
	if g.shadow == nil {
		return
	}
	epoch := g.appliedEpoch.Load()
	for _, channel := range g.reg.allChannels() {
		for i := 0; i < shadowOutcomeCount; i++ {
			outcome := shadowCleanHTTP200
			if i%4 == 0 {
				outcome = shadowHTTPError
			}
			token := g.shadow.Begin(epoch, uint(channel.id), now)
			g.shadow.End(&token, outcome, now)
		}
	}
}

// Isolate the actual production selection call from body parsing and SQLite.
// Seeded draw/call-sequence equivalence is checked separately by integration tests.
func BenchmarkShadowProductionSelection(b *testing.B) {
	for _, size := range []int{2, 8} {
		for _, enabled := range []bool{false, true} {
			b.Run(fmt.Sprintf("pool%d/enabled=%t", size, enabled), func(b *testing.B) {
				g, err := newRegisteredGateway(Options{Channels: shadowIntegrationChannels(size), RouteDiagnostics: true, ShadowRouteScoring: enabled})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(g.Close)
				c := newOutputScope(outputIdentity(), &observerOutput{}, newRequestResult(outputIdentity()), "")
				c.source = invocationForeground
				candidates := g.reg.allChannels()
				now := time.Now()
				warmShadowBenchmark(g, now)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					g.selectTargetScope(c, candidates, nil, "", now)
				}
			})
		}
	}
}

func TestShadowProductionSelectionLatencyReport(t *testing.T) {
	if testing.Short() {
		t.Skip("standalone host timing report")
	}
	for _, size := range []int{2, 8} {
		for _, enabled := range []bool{false, true} {
			g, err := newRegisteredGateway(Options{Channels: shadowIntegrationChannels(size), RouteDiagnostics: true, ShadowRouteScoring: enabled})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			c := newOutputScope(outputIdentity(), &observerOutput{}, newRequestResult(outputIdentity()), "")
			c.source = invocationForeground
			candidates, now := g.reg.allChannels(), time.Now()
			warmShadowBenchmark(g, now)
			for i := 0; i < 256; i++ {
				g.selectTargetScope(c, candidates, nil, "", now)
			}
			const iterations = 10000
			durations := make([]int64, iterations)
			for i := range durations {
				started := time.Now()
				g.selectTargetScope(c, candidates, nil, "", now)
				durations[i] = time.Since(started).Nanoseconds()
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if durations[iterations*95/100] == 0 {
				t.Logf("production selection pool=%d shadow=%t samples=%d: clock resolution cannot resolve per-operation quantiles", size, enabled, iterations)
				continue
			}
			t.Logf("production selection pool=%d shadow=%t samples=%d p50_ns=%d p95_ns=%d", size, enabled, iterations, durations[iterations/2], durations[iterations*95/100])
		}
	}
}
