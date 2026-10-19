package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShadowFixedReasonsAndExistingMetrics(t *testing.T) {
	r := New()
	r.RecordRequest("m", "c", 200, time.Second)
	before := render(t, r)
	if strings.Contains(before, "gateway_shadow_") {
		t.Fatal("unused shadow metrics changed ordinary exposition")
	}
	for _, reason := range shadowReasons[:10] {
		r.RecordShadowComparison(reason)
	}
	for _, reason := range []string{"", "target-id-secret", "bad\"}\nnew_metric 1", "unknown"} {
		r.RecordShadowComparison(reason)
	}
	r.RecordShadowSample(false)
	r.RecordShadowSample(true)
	out := render(t, r)
	if !strings.HasSuffix(out, before) {
		t.Fatal("existing metric output changed")
	}
	if strings.Contains(out, "target-id-secret") || strings.Contains(out, "new_metric") {
		t.Fatal("arbitrary reason escaped fixed labels")
	}
	for _, reason := range shadowReasons[:10] {
		if !strings.Contains(out, fmt.Sprintf("gateway_shadow_comparisons_total{reason=%q} 1\n", reason)) {
			t.Fatal("missing fixed reason", reason)
		}
	}
	for _, want := range []string{
		"gateway_shadow_comparisons_total{reason=\"unknown\"} 4\n",
		"gateway_shadow_eligible_total 7\n", "gateway_shadow_compared_total 2\n", "gateway_shadow_matched_total 1\n",
		"gateway_shadow_samples_total{outcome=\"clean_http_200\"} 1\n", "gateway_shadow_samples_total{outcome=\"http_error\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatal("missing accounting", want)
		}
	}
	if got := strings.Count(out, "gateway_shadow_comparisons_total{reason="); got != 11 {
		t.Fatal("unbounded reason series", got)
	}
}

func TestShadowSampleEnablesAllFixedZeroSeries(t *testing.T) {
	r := New()
	r.RecordShadowSample(true)
	out := render(t, r)
	for _, reason := range shadowReasons {
		if !strings.Contains(out, fmt.Sprintf("gateway_shadow_comparisons_total{reason=%q} 0\n", reason)) {
			t.Fatal("missing zero reason", reason)
		}
	}
	for _, want := range []string{"gateway_shadow_eligible_total 0\n", "gateway_shadow_compared_total 0\n", "gateway_shadow_matched_total 0\n", "gateway_shadow_samples_total{outcome=\"clean_http_200\"} 0\n"} {
		if !strings.Contains(out, want) {
			t.Fatal("missing zero counter", want)
		}
	}
}

func TestShadowConcurrentAccounting(t *testing.T) {
	r := New()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 1000; i++ {
				r.RecordShadowComparison("compared_equal")
				r.RecordShadowComparison("slots_busy")
				r.RecordShadowSample(i%2 == 0)
			}
		}()
	}
	workers.Wait()
	out := render(t, r)
	for _, want := range []string{"gateway_shadow_eligible_total 16000\n", "gateway_shadow_compared_total 8000\n", "gateway_shadow_matched_total 8000\n", "gateway_shadow_samples_total{outcome=\"clean_http_200\"} 4000\n", "gateway_shadow_samples_total{outcome=\"http_error\"} 4000\n"} {
		if !strings.Contains(out, want) {
			t.Fatal("lost concurrent count", want)
		}
	}
}

func TestShadowRecordingDoesNotAllocate(t *testing.T) {
	r := New()
	if allocations := testing.AllocsPerRun(1000, func() {
		r.RecordShadowComparison("compared_equal")
		r.RecordShadowComparison("untrusted-reason")
		r.RecordShadowSample(false)
		r.RecordShadowSample(true)
	}); allocations != 0 {
		t.Fatal("shadow observation allocated", allocations)
	}
}
