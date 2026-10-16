package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestAttemptAndLogQueueMetricsRemainDistinctFromRequests(t *testing.T) {
	r := New()
	r.RecordAttempt("upstream", "upstream_error", 429, time.Millisecond)
	r.RecordAttempt("upstream", "success", 200, 2*time.Millisecond)
	r.RecordRequest("m1", "upstream", 200, 3*time.Millisecond)
	r.SetLogStatsProvider(func() LogQueueStats {
		return LogQueueStats{Depth: 3, Capacity: 4096, Dropped: 2, Failed: 1}
	})
	var out strings.Builder
	if err := r.Render(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`gateway_attempts_total{channel="upstream",status="429",termination="upstream_error"} 1`,
		`gateway_attempts_total{channel="upstream",status="200",termination="success"} 1`,
		`gateway_attempt_duration_seconds_count{channel="upstream"} 2`,
		`gateway_requests_total{channel="upstream",model="m1",status="200"} 1`,
		"gateway_log_queue_depth 3", "gateway_log_queue_capacity 4096",
		"gateway_logs_dropped_total 2", "gateway_logs_failed_total 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing metric %s", want)
		}
	}
}
