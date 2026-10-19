package gateway

import (
	"encoding/json"
	"log/slog"
	"time"

	"minigateway/internal/core"
)

// The caller owns the registry lock; sampler methods never run under that lock.
func (g *Gateway) publishShadowEpochLocked(registry *registry, initial bool) {
	epoch := uint64(1)
	if !initial {
		current := g.appliedEpoch.Load()
		if current == 0 || current == ^uint64(0) {
			epoch = 0 // Overflow disables only shadow observation until restart.
		} else {
			epoch = current + 1
		}
	}
	for _, channel := range registry.channels {
		channel.epoch = epoch
	}
	g.appliedEpoch.Store(epoch)
}

func (g *Gateway) shadowTime() time.Time {
	if g.shadowNow != nil {
		return g.shadowNow()
	}
	return time.Now()
}

func classifyShadowHTTP(raw core.Result) shadowOutcome {
	if raw.Phase != core.ResponseReceived || raw.StatusCode != raw.UpstreamStatus {
		return shadowIgnored
	}
	if raw.UpstreamStatus == 200 && raw.Termination == core.Success && raw.ProtocolComplete && raw.Error == nil {
		return shadowCleanHTTP200
	}
	if raw.UpstreamStatus < 400 || raw.UpstreamStatus > 599 || raw.Termination != core.UpstreamError {
		return shadowIgnored
	}
	// Frozen core has no typed error-body read fault. This host-owned fixed
	// message conservatively excludes its generated 502, including real 502s
	// that happen to reuse the same detail, instead of inventing an observation.
	if raw.Error != nil && raw.Error.Error.Type == string(core.UpstreamError) && raw.Error.Error.Message == "failed reading upstream error" {
		return shadowIgnored
	}
	return shadowHTTPError
}

type shadowDebugCandidate struct {
	ChannelID   uint   `json:"channel_id"`
	Active      uint64 `json:"active"`
	Live        int    `json:"live"`
	Errors      int    `json:"errors"`
	Neutral     bool   `json:"neutral"`
	Numerator   uint64 `json:"numerator"`
	Denominator uint64 `json:"denominator"`
}

func (g *Gateway) compareShadowRoute(c *executionScope, decision routeDecision, selected *channel, now time.Time) {
	if g.shadow == nil || c.source != invocationForeground {
		return
	}
	epoch, actual := g.appliedEpoch.Load(), uint(0)
	if selected != nil {
		epoch, actual = selected.epoch, uint(selected.id)
	}
	comparison := g.shadow.Compare(epoch, decision.shadowPool, decision.selectionPoolChannels, c.responseTarget != nil, actual, now)
	if g.metrics != nil {
		g.metrics.RecordShadowComparison(comparison.reason)
	}
	if !slog.Default().Enabled(c.ctx, slog.LevelDebug) {
		return
	}
	g.shadowLogMu.Lock()
	if !g.shadowLastLog.IsZero() && now.Sub(g.shadowLastLog) < time.Second {
		g.shadowLogMu.Unlock()
		return
	}
	g.shadowLastLog = now
	g.shadowLogMu.Unlock()
	var candidates [8]shadowDebugCandidate
	count := max(0, min(comparison.count, len(candidates)))
	if !comparison.compared {
		count = 0
	}
	for i, candidate := range comparison.candidates[:count] {
		candidates[i] = shadowDebugCandidate{candidate.channelID, candidate.active, candidate.live, candidate.errors, candidate.neutral, candidate.numerator, candidate.denominator}
	}
	encoded, _ := json.Marshal(candidates[:count])
	slog.DebugContext(c.ctx, "shadow route comparison", "actual_channel_id", actual, "recommended_channel_id", comparison.recommended,
		"pool_size", decision.selectionPoolChannels, "epoch", comparison.epoch, "reason", comparison.reason,
		"compared", comparison.compared, "matched", comparison.matched, "candidates", string(encoded))
}
