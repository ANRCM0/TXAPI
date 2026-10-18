package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRouteSelectionMetricsUseMicrosecondBuckets(t *testing.T) {
	r := New()
	r.RecordRouteSelection("selected", "weighted", 8*time.Microsecond)
	r.RecordRequest("m", "c", 200, 20*time.Millisecond)
	out := render(t, r)
	for _, want := range []string{
		`gateway_route_selections_total{outcome="selected",picker="weighted"} 1`,
		`gateway_route_selection_duration_seconds_bucket{le="1e-06",picker="weighted"} 0`,
		`gateway_route_selection_duration_seconds_bucket{le="1e-05",picker="weighted"} 1`,
		`gateway_route_selection_duration_seconds_sum{picker="weighted"} 0.000008`,
		`gateway_route_selection_duration_seconds_count{picker="weighted"} 1`,
		`gateway_request_duration_seconds_bucket{channel="c",le="0.05",model="m"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing routing/unchanged request histogram fact %q: %s", want, out)
		}
	}
}

func TestRouteSelectionMetricsBoundLabelCardinality(t *testing.T) {
	r := New()
	for i := range 100 {
		r.RecordRouteSelection(fmt.Sprintf("untrusted-outcome-%d", i), fmt.Sprintf("untrusted-picker-%d", i), -time.Second)
	}
	out := render(t, r)
	if len(r.routeSelections) != 1 || len(r.routeDurations) != 1 || strings.Contains(out, "untrusted-") ||
		!strings.Contains(out, `gateway_route_selections_total{outcome="unknown",picker="unknown"} 100`) ||
		!strings.Contains(out, `gateway_route_selection_duration_seconds_sum{picker="unknown"} 0.000000`) {
		t.Fatalf("routing labels or latency accepted unbounded caller input: %s", out)
	}
}

func TestRouteSelectionConcurrentObservationsAreCountedOnce(t *testing.T) {
	r := New()
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				r.RecordRouteSelection("selected", "extension", time.Microsecond)
			}
		}()
	}
	workers.Wait()
	out := render(t, r)
	for _, want := range []string{
		`gateway_route_selections_total{outcome="selected",picker="extension"} 800`,
		`gateway_route_selection_duration_seconds_count{picker="extension"} 800`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("lost or duplicated concurrent routing observations: %s", out)
		}
	}
}
