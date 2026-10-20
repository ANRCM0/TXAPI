package metrics

import (
	"fmt"
	"strings"
)

// RuntimeStats contains aggregate owner snapshots, not execution or recovery
// authority. Fields from different owners need not describe the same instant.
type RuntimeStats struct {
	Requests, Attempts                  int
	NativeWSEnabled                     bool
	NativeWSConnections, NativeWSRounds int
}

// SetRuntimeStatsProvider installs a read-only, bounded in-memory snapshot.
// The provider must not perform I/O or call back into this registry.
func (r *Registry) SetRuntimeStatsProvider(fn func() RuntimeStats) {
	r.mu.Lock()
	r.runtimeStats = fn
	r.mu.Unlock()
}

func (r *Registry) renderRuntime(b *strings.Builder) {
	if r.runtimeStats == nil {
		return
	}
	s := r.runtimeStats()
	enabled := 0
	if s.NativeWSEnabled {
		enabled = 1
	}
	for _, metric := range []struct {
		name, help string
		value      int
	}{
		{"gateway_admission_requests_active", "Occupied generation request admission slots, across transports", s.Requests},
		{"gateway_admission_attempts_active", "Occupied upstream attempt admission slots, across transports", s.Attempts},
		{"gateway_native_ws_enabled", "Native WebSocket runtime configured in this process", enabled},
		{"gateway_native_ws_connections_active", "Authorized native WebSocket sessions whose Close has not finished; not TCP or upstream pool connections", s.NativeWSConnections},
		{"gateway_native_ws_rounds_active", "Native WebSocket Execute rounds including authorization, admission and final cleanup", s.NativeWSRounds},
	} {
		writeFamilyHeader(b, metric.name, "gauge", metric.help)
		fmt.Fprintf(b, "%s %d\n", metric.name, metric.value)
	}
}
