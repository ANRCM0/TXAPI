package generation

import (
	bg "minigateway/internal/foundation/contracts/background/v1"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func legacyBackgroundUsage(u *v1.Usage) *v3.UsageObservation {
	if u == nil {
		return &v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
	}
	i, o, t := int64(u.PromptTokens), int64(u.CompletionTokens), int64(u.TotalTokens)
	return &v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: &i, OutputTokens: &o, TotalTokens: &t}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "background_legacy_tokens_v1"}
}

// Merge cumulative snapshots by filling only unknown dimensions. A resumed
// prefix or missing usage never erases a previously accepted quantity.
func mergeBackgroundUsage(old, incoming *v3.UsageObservation) (*v3.UsageObservation, error) {
	if incoming == nil {
		incoming = &v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
	}
	if incoming.Validate() != nil || old != nil && old.Validate() != nil {
		return nil, bg.ErrCursorUnavailable
	}
	result := *incoming
	if old != nil && old.Source == v2.ProviderReported {
		if result.Source == v2.SourceUnknown {
			result.Source, result.Profile = old.Source, old.Profile
		} else if old.Profile != result.Profile && old.Profile != "background_legacy_tokens_v1" {
			return nil, bg.ErrCursorUnavailable
		}
	}
	var previous [6]*int64
	if old != nil {
		previous = [6]*int64{old.Counts.InputTokens, old.Counts.OutputTokens, old.Counts.TotalTokens, old.Breakdown.CachedInputTokens, old.Breakdown.CacheWriteInputTokens, old.Breakdown.ReasoningOutputTokens}
	}
	fields := [6]**int64{&result.Counts.InputTokens, &result.Counts.OutputTokens, &result.Counts.TotalTokens, &result.Breakdown.CachedInputTokens, &result.Breakdown.CacheWriteInputTokens, &result.Breakdown.ReasoningOutputTokens}
	known, baseKnown := 0, 0
	for i, field := range fields {
		if previous[i] != nil {
			if *field != nil && **field != *previous[i] {
				return nil, bg.ErrCursorUnavailable
			}
			*field = previous[i]
		}
		if *field != nil {
			n := **field
			*field = &n
			known++
			if i < 3 {
				baseKnown++
			}
		}
	}
	switch {
	case known == 0:
		result.Completeness = v2.Unknown
	case baseKnown == 3:
		result.Completeness = v2.Complete
	default:
		result.Completeness = v2.Partial
	}
	if result.Validate() != nil {
		return nil, bg.ErrCursorUnavailable
	}
	return &result, nil
}
