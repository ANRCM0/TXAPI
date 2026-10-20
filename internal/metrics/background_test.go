package metrics

import (
	"strings"
	"testing"
)

func TestBackgroundMetricsSeparateUnknownFailureAndKnownZero(t *testing.T) {
	r := New()
	if strings.Contains(render(t, r), "gateway_background_") {
		t.Fatal("unconfigured background metrics were exposed")
	}
	current := BackgroundStats{Enabled: true, ActiveControls: 1}
	r.SetBackgroundStatsProvider(func() BackgroundStats { return current })
	initial := render(t, r)
	for _, value := range []string{"gateway_background_enabled 1\n", "gateway_background_controls_active 1\n", "gateway_background_snapshot_success 0\n", "gateway_background_snapshot_timestamp_seconds 0\n"} {
		if !strings.Contains(initial, value) {
			t.Fatal("unknown sample is not explicit", value, initial)
		}
	}
	if strings.Contains(initial, "gateway_background_jobs") || strings.Contains(initial, "gateway_background_events_retained") {
		t.Fatal("unknown durable occupancy fabricated zeros")
	}
	current = BackgroundStats{SampleSuccess: true, LastSuccessUnixSeconds: 123, Queued: 1, Terminal: 2, Events: 3, EventBytes: 101, ReservedBytes: 4096}
	for _, success := range []bool{true, false} {
		current.SampleSuccess = success
		out := render(t, r)
		for _, value := range []string{"gateway_background_enabled 0\n", "gateway_background_snapshot_timestamp_seconds 123\n", `gateway_background_jobs{stage="queued"} 1`, `gateway_background_jobs{stage="terminal"} 2`, `gateway_background_jobs{stage="other"} 0`, "gateway_background_events_retained 3\n", "gateway_background_event_bytes 101\n", "gateway_background_reserved_bytes 4096\n"} {
			if !strings.Contains(out, value) {
				t.Fatal("failed sample lost last known counts or fixed stages", value, out)
			}
		}
		if strings.Count(out, "gateway_background_jobs{stage=") != 6 {
			t.Fatal("stage label cardinality changed", out)
		}
	}
	current = BackgroundStats{SampleSuccess: true, LastSuccessUnixSeconds: 124}
	if out := render(t, r); !strings.Contains(out, "gateway_background_events_retained 0\n") {
		t.Fatal("known zero omitted", out)
	}
	r.SetBackgroundStatsProvider(nil)
	if strings.Contains(render(t, r), "gateway_background_") {
		t.Fatal("removed background provider retained values")
	}
}
