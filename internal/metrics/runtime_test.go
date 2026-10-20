package metrics

import (
	"fmt"
	"strings"
	"testing"
)

func TestRuntimeMetricsReadCurrentOwnerSnapshot(t *testing.T) {
	r := New()
	if strings.Contains(render(t, r), "gateway_native_ws_enabled") {
		t.Fatal("unconfigured provider fabricated a runtime snapshot")
	}
	current := RuntimeStats{Requests: 3, Attempts: 2, NativeWSEnabled: true, NativeWSConnections: 4, NativeWSRounds: 1}
	r.SetRuntimeStatsProvider(func() RuntimeStats { return current })
	for _, values := range [][5]int{{3, 2, 1, 4, 1}, {0, 0, 0, 0, 0}} {
		out := render(t, r)
		for i, name := range []string{"gateway_admission_requests_active", "gateway_admission_attempts_active", "gateway_native_ws_enabled", "gateway_native_ws_connections_active", "gateway_native_ws_rounds_active"} {
			if strings.Count(out, "\n"+name+" ") != 1 || !strings.Contains(out, "# TYPE "+name+" gauge\n") || !strings.Contains(out, fmt.Sprintf("\n%s %d\n", name, values[i])) || strings.Contains(out, name+"{") {
				t.Fatalf("invalid aggregate gauge %s: %s", name, out)
			}
		}
		current = RuntimeStats{}
	}
	r.SetRuntimeStatsProvider(nil)
	if strings.Contains(render(t, r), "gateway_native_ws_enabled") {
		t.Fatal("removed provider still reported a snapshot")
	}
}
