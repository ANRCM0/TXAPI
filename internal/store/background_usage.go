package store

import (
	bg "minigateway/internal/foundation/contracts/background/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundUsage(row model.BackgroundUsage) *v3.UsageObservation {
	if row.Completeness == "" {
		return nil
	}
	return &v3.UsageObservation{
		Counts:       v2.TokenCounts{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalTokens: row.TotalTokens},
		Breakdown:    v3.TokenBreakdown{CachedInputTokens: row.CachedInputTokens, CacheWriteInputTokens: row.CacheWriteInputTokens, ReasoningOutputTokens: row.ReasoningOutputTokens},
		Completeness: v2.Completeness(row.Completeness), Source: v2.UsageSource(row.Source), Profile: row.Profile,
	}
}

func backgroundUsageRow(u *v3.UsageObservation) model.BackgroundUsage {
	if u == nil {
		return model.BackgroundUsage{}
	}
	return model.BackgroundUsage{InputTokens: u.Counts.InputTokens, OutputTokens: u.Counts.OutputTokens, TotalTokens: u.Counts.TotalTokens, CachedInputTokens: u.Breakdown.CachedInputTokens, CacheWriteInputTokens: u.Breakdown.CacheWriteInputTokens, ReasoningOutputTokens: u.Breakdown.ReasoningOutputTokens, Completeness: string(u.Completeness), Source: string(u.Source), Profile: u.Profile}
}

func backgroundProtocolStatus(state bg.RemoteState) string {
	return map[bg.RemoteState]string{bg.RemoteAccepted: "queued", bg.RemoteRunning: "in_progress", bg.RemoteSucceeded: "completed", bg.RemoteIncomplete: "incomplete", bg.RemoteFailed: "failed", bg.RemoteCancelled: "cancelled"}[state]
}

func backgroundUsageView(row model.BackgroundUsage) *history.UsageView {
	if row.Completeness == "" {
		return nil
	}
	return &history.UsageView{InputTokens: usageString(row.InputTokens), OutputTokens: usageString(row.OutputTokens), TotalTokens: usageString(row.TotalTokens), CachedInputTokens: usageString(row.CachedInputTokens), CacheWriteInputTokens: usageString(row.CacheWriteInputTokens), ReasoningOutputTokens: usageString(row.ReasoningOutputTokens), Completeness: row.Completeness, Source: row.Source, Profile: row.Profile}
}

// This independently protects the persisted projection at its transaction owner.
// A caller cannot clear known quantities or regress a terminal native state.
func applyBackgroundUsage(row *model.BackgroundJob, metadata bs.UsageMetadata, stage bg.Stage) error {
	status := backgroundProtocolStatus(metadata.NativeState)
	terminal := status == "completed" || status == "incomplete" || status == "failed" || status == "cancelled"
	if stage != bg.StageRemoteActive && stage != bg.StageTerminal || metadata.NativeState != "" && status == "" || terminal != (stage == bg.StageTerminal) || metadata.Usage != nil && (!terminal || metadata.Usage.Validate() != nil) {
		return bg.ErrConflict
	}
	oldState := bg.RemoteState(row.NativeState)
	if oldState != "" && oldState != bg.RemoteAccepted && oldState != bg.RemoteRunning && oldState != metadata.NativeState {
		return bg.ErrConflict
	}
	if old := backgroundUsage(row.Usage); old != nil {
		if metadata.Usage == nil {
			return bg.ErrConflict
		}
		next := metadata.Usage
		before := [6]*int64{old.Counts.InputTokens, old.Counts.OutputTokens, old.Counts.TotalTokens, old.Breakdown.CachedInputTokens, old.Breakdown.CacheWriteInputTokens, old.Breakdown.ReasoningOutputTokens}
		after := [6]*int64{next.Counts.InputTokens, next.Counts.OutputTokens, next.Counts.TotalTokens, next.Breakdown.CachedInputTokens, next.Breakdown.CacheWriteInputTokens, next.Breakdown.ReasoningOutputTokens}
		for i, count := range before {
			if count != nil && (after[i] == nil || *count != *after[i]) {
				return bg.ErrConflict
			}
		}
	}
	row.NativeState, row.Usage = string(metadata.NativeState), backgroundUsageRow(metadata.Usage)
	return nil
}
