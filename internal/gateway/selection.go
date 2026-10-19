package gateway

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/model"
	"minigateway/internal/scheduler"
)

type routeDecision struct {
	outcome                string
	picker                 string
	candidateChannels      int
	eligibleChannels       int
	selectionPoolChannels  int
	totalCredentials       int
	triedCredentials       int
	unavailableCredentials int
	eligibleCredentials    int
	priorityFallback       bool
	selectedChannel        int
	selectedCredential     uint
}

func (g *Gateway) selectTargetContext(ctx context.Context, candidates []*channel, tried map[uint]bool, session string, now time.Time) (*channel, *model.Credential) {
	started := time.Now()
	ch, credential, decision := g.chooseTarget(candidates, tried, session, now)
	g.recordRouteDecision(ctx, decision, time.Since(started))
	return ch, credential
}

func (g *Gateway) recordRouteDecision(ctx context.Context, decision routeDecision, duration time.Duration) {
	if g.metrics != nil {
		g.metrics.RecordRouteSelection(decision.outcome, decision.picker, duration)
	}
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		slog.DebugContext(ctx, "route selection",
			"request_id", access.RequestID(ctx), "outcome", decision.outcome, "picker", decision.picker,
			"candidate_channels", decision.candidateChannels, "eligible_channels", decision.eligibleChannels,
			"selection_pool_channels", decision.selectionPoolChannels, "total_credentials", decision.totalCredentials,
			"tried_credentials", decision.triedCredentials, "unavailable_credentials", decision.unavailableCredentials,
			"eligible_credentials", decision.eligibleCredentials, "priority_fallback", decision.priorityFallback,
			"channel_id", decision.selectedChannel, "credential_id", decision.selectedCredential,
			"selection_duration_seconds", duration.Seconds())
	}
}

func (g *Gateway) recordScopeRouteDecision(c *executionScope, decision routeDecision, duration time.Duration) {
	g.recordRouteDecision(c.ctx, decision, duration)
	if c.routeCapture != nil {
		c.routeCapture.selectTarget(decision, duration)
	}
}

func (g *Gateway) selectTargetScope(c *executionScope, candidates []*channel, tried map[uint]bool, session string, now time.Time) (*channel, *model.Credential) {
	started := time.Now()
	ch, credential, decision := g.chooseTarget(candidates, tried, session, now)
	g.recordScopeRouteDecision(c, decision, time.Since(started))
	return ch, credential
}

func (g *Gateway) chooseTarget(candidates []*channel, tried map[uint]bool, session string, now time.Time) (*channel, *model.Credential, routeDecision) {
	d := routeDecision{outcome: "no_candidates", picker: "none", candidateChannels: len(candidates)}
	if len(candidates) == 0 {
		return nil, nil, d
	}
	d.outcome = "no_usable_credentials"
	maxPriority, highestPriority := 0, candidates[0].priority
	first := true
	for _, ch := range candidates {
		if ch.priority > highestPriority {
			highestPriority = ch.priority
		}
		eligible := 0
		for _, credential := range ch.credentials {
			d.totalCredentials++
			switch {
			case tried[credential.ID]:
				d.triedCredentials++
			case credential.APIKey == "" || !credential.IsUsable(now):
				d.unavailableCredentials++
			default:
				eligible++
			}
		}
		if eligible == 0 {
			continue
		}
		d.eligibleChannels++
		d.eligibleCredentials += eligible
		if first || ch.priority > maxPriority {
			maxPriority, first = ch.priority, false
		}
	}
	pool := make([]*channel, 0, len(candidates))
	for _, ch := range candidates {
		if ch.priority == maxPriority && hasUntriedCredential(ch, tried, now) {
			pool = append(pool, ch)
		}
	}
	d.selectionPoolChannels = len(pool)
	if len(pool) == 0 {
		return nil, nil, d
	}
	ch := chooseWeightedChannel(pool, rand.Intn)
	credential, picker := g.pickCredentialDecision(ch, tried, session, now)
	d.picker = picker
	if credential == nil {
		d.outcome = "credential_selection_failed"
		return nil, nil, d
	}
	d.outcome, d.selectedChannel, d.selectedCredential = "selected", ch.id, credential.ID
	d.priorityFallback = ch.priority < highestPriority
	return ch, credential, d
}

func (g *Gateway) pickCredentialDecision(ch *channel, tried map[uint]bool, session string, now time.Time) (*model.Credential, string) {
	method := "weighted"
	if g.picker != nil {
		// An extension chooses an ID; credential material remains owned by this snapshot.
		excluded := make(map[uint]bool, len(tried))
		for id, used := range tried {
			excluded[id] = used
		}
		chosen, ok := g.picker(append([]model.Credential(nil), ch.credentials...), excluded, session, now)
		if ok && chosen != nil {
			for _, candidate := range ch.credentials {
				if candidate.ID == chosen.ID && !tried[candidate.ID] && candidate.IsUsable(now) && candidate.APIKey != "" {
					return &candidate, "extension"
				}
			}
		}
		method = "weighted_fallback"
	}
	selected, ok := scheduler.Pick(ch.credentials, tried, now)
	if !ok {
		return nil, method
	}
	return selected, method
}
