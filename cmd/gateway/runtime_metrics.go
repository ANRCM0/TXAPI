package main

import (
	"minigateway/internal/access"
	"minigateway/internal/metrics"
)

func registerRuntimeMetrics(registry *metrics.Registry, capacity *access.Capacity, native *responsesWSRuntime) {
	registry.SetRuntimeStatsProvider(func() metrics.RuntimeStats {
		slots := capacity.Snapshot()
		out := metrics.RuntimeStats{Requests: slots.Requests, Attempts: slots.Attempts}
		if native != nil {
			ws := native.service.Occupancy()
			out.NativeWSEnabled = true
			out.NativeWSConnections, out.NativeWSRounds = ws.Connections, ws.Rounds
		}
		return out
	})
}
