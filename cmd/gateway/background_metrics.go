package main

import (
	"context"
	"time"

	"minigateway/internal/metrics"
	"minigateway/internal/store"
)

type backgroundOccupancySample struct {
	Occupancy store.BackgroundOccupancy
	Success   bool
	UnixTime  int64
}

// The existing retention loop is the sole sampler. Scrapes only load this
// immutable value, so a slow or unavailable database cannot stall /metrics.
func (r *responsesRuntime) sampleBackgroundOccupancy(ctx context.Context, st *store.Store) {
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := st.BackgroundOccupancy(bounded)
	var next backgroundOccupancySample
	if previous := r.backgroundOccupancy.Load(); previous != nil {
		next = *previous
	}
	next.Success = err == nil
	if err == nil {
		next.Occupancy = value
		next.UnixTime = time.Now().Unix()
	}
	r.backgroundOccupancy.Store(&next)
}

func registerBackgroundMetrics(registry *metrics.Registry, runtime *responsesRuntime) {
	if runtime == nil {
		registry.SetBackgroundStatsProvider(nil)
		return
	}
	registry.SetBackgroundStatsProvider(func() metrics.BackgroundStats {
		out := metrics.BackgroundStats{Enabled: runtime.worker != nil, ActiveControls: runtime.worker.ActiveControls()}
		if cached := runtime.backgroundOccupancy.Load(); cached != nil {
			v := cached.Occupancy
			out.SampleSuccess, out.LastSuccessUnixSeconds = cached.Success, cached.UnixTime
			out.Queued, out.Dispatching, out.RemoteActive = v.Queued, v.Dispatching, v.RemoteActive
			out.Terminal, out.Uncertain, out.Other = v.Terminal, v.Uncertain, v.Other
			out.ReplayPending, out.Events, out.EventBytes, out.ReservedBytes = v.ReplayPending, v.Events, v.EventBytes, v.ReservedBytes
		}
		return out
	})
}
