package metrics

import (
	"fmt"
	"strings"
)

// BackgroundStats combines a current process control count with a cached,
// timestamped database observation. Scraping must not perform database I/O.
type BackgroundStats struct {
	Enabled, SampleSuccess                                        bool
	ActiveControls, LastSuccessUnixSeconds                        int64
	Queued, Dispatching, RemoteActive, Terminal, Uncertain, Other int64
	ReplayPending, Events, EventBytes, ReservedBytes              int64
}

func (r *Registry) SetBackgroundStatsProvider(fn func() BackgroundStats) {
	r.mu.Lock()
	r.backgroundStats = fn
	r.mu.Unlock()
}

func (r *Registry) renderBackground(b *strings.Builder) {
	if r.backgroundStats == nil {
		return
	}
	s := r.backgroundStats()
	enabled, success := int64(0), int64(0)
	if s.Enabled {
		enabled = 1
	}
	if s.SampleSuccess {
		success = 1
	}
	for _, metric := range []struct {
		name, help string
		value      int64
	}{
		{"gateway_background_enabled", "Native background control runtime configured in this process", enabled},
		{"gateway_background_controls_active", "Claimed background controls including final release cleanup; not remote generation or valid lease counts", s.ActiveControls},
		{"gateway_background_snapshot_success", "Whether the last bounded metadata sample succeeded; check the timestamp for freshness", success},
		{"gateway_background_snapshot_timestamp_seconds", "Unix time of the last successful retained metadata sample; zero means never sampled", s.LastSuccessUnixSeconds},
	} {
		writeFamilyHeader(b, metric.name, "gauge", metric.help)
		fmt.Fprintf(b, "%s %d\n", metric.name, metric.value)
	}
	if s.LastSuccessUnixSeconds == 0 {
		return
	}
	writeFamilyHeader(b, "gateway_background_jobs", "gauge", "Retained background metadata records, including tombstones, from the last successful sample")
	for _, stage := range []struct {
		name  string
		value int64
	}{{"queued", s.Queued}, {"dispatching", s.Dispatching}, {"remote_active", s.RemoteActive}, {"terminal", s.Terminal}, {"uncertain", s.Uncertain}, {"other", s.Other}} {
		fmt.Fprintf(b, "gateway_background_jobs{stage=\"%s\"} %d\n", stage.name, stage.value)
	}
	for _, metric := range []struct {
		name, help string
		value      int64
	}{
		{"gateway_background_replay_pending", "Retained tasks requiring native event-tail recovery, from the last successful sample", s.ReplayPending},
		{"gateway_background_events_retained", "Retained encrypted event count recorded atomically with task metadata, from the last successful sample", s.Events},
		{"gateway_background_event_bytes", "Retained encrypted event payload bytes, excluding SQLite and WAL overhead, from the last successful sample", s.EventBytes},
		{"gateway_background_reserved_bytes", "Reserved background content capacity, not physical database size, from the last successful sample", s.ReservedBytes},
	} {
		writeFamilyHeader(b, metric.name, "gauge", metric.help)
		fmt.Fprintf(b, "%s %d\n", metric.name, metric.value)
	}
}
