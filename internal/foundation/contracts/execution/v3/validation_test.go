package execution_test

import (
	"math"
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func quantity(value int64) *int64 { return &value }

func providerUsage(counts v2.TokenCounts, details v3.TokenBreakdown, completeness v2.Completeness) v3.UsageObservation {
	return v3.UsageObservation{Counts: counts, Breakdown: details, Completeness: completeness, Source: v2.ProviderReported, Profile: "responses_native_tokens_v2"}
}

func TestTokenObservationPreservesOptionalAndExplicitZero(t *testing.T) {
	for name, observation := range map[string]v3.UsageObservation{
		"unknown":           {Completeness: v2.Unknown, Source: v2.SourceUnknown},
		"known zero totals": providerUsage(v2.TokenCounts{InputTokens: quantity(0), OutputTokens: quantity(0), TotalTokens: quantity(0)}, v3.TokenBreakdown{}, v2.Complete),
		"known zero detail": providerUsage(v2.TokenCounts{}, v3.TokenBreakdown{CachedInputTokens: quantity(0)}, v2.Partial),
		"partial base":      providerUsage(v2.TokenCounts{InputTokens: quantity(9)}, v3.TokenBreakdown{}, v2.Partial),
		"bounded partial total": providerUsage(v2.TokenCounts{TotalTokens: quantity(7)},
			v3.TokenBreakdown{CachedInputTokens: quantity(2), CacheWriteInputTokens: quantity(3), ReasoningOutputTokens: quantity(2)}, v2.Partial),
		"bounded details": providerUsage(v2.TokenCounts{InputTokens: quantity(9), OutputTokens: quantity(4), TotalTokens: quantity(13)},
			v3.TokenBreakdown{CachedInputTokens: quantity(3), CacheWriteInputTokens: quantity(6), ReasoningOutputTokens: quantity(4)}, v2.Complete),
		"max base": providerUsage(v2.TokenCounts{InputTokens: quantity(math.MaxInt64), OutputTokens: quantity(0), TotalTokens: quantity(math.MaxInt64)}, v3.TokenBreakdown{}, v2.Complete),
	} {
		t.Run(name, func(t *testing.T) {
			if err := observation.Validate(); err != nil {
				t.Fatal(err)
			}
			if name == "known zero detail" && (observation.Counts.InputTokens != nil || observation.Breakdown.CachedInputTokens == nil) {
				t.Fatal("validation inferred an unknown count or erased zero")
			}
		})
	}
}

func TestTokenObservationRejectsInconsistentProvenanceAndBounds(t *testing.T) {
	complete := providerUsage(v2.TokenCounts{InputTokens: quantity(9), OutputTokens: quantity(4), TotalTokens: quantity(13)}, v3.TokenBreakdown{}, v2.Complete)
	badSource, badProfile, badCompleteness := complete, complete, complete
	badSource.Source = v2.SourceUnknown
	badProfile.Profile = "not a declared profile"
	badCompleteness.Completeness = v2.Partial
	tooLong := complete
	tooLong.Profile = strings.Repeat("x", 65)
	for name, observation := range map[string]v3.UsageObservation{
		"zero value metadata":               {},
		"unknown has source":                {Completeness: v2.Unknown, Source: v2.ProviderReported},
		"bad source":                        badSource,
		"bad profile":                       badProfile,
		"profile too long":                  tooLong,
		"bad completeness":                  badCompleteness,
		"incomplete totals called complete": providerUsage(v2.TokenCounts{InputTokens: quantity(1)}, v3.TokenBreakdown{}, v2.Complete),
		"wrong total":                       providerUsage(v2.TokenCounts{InputTokens: quantity(1), OutputTokens: quantity(2), TotalTokens: quantity(4)}, v3.TokenBreakdown{}, v2.Complete),
		"base overflow without total":       providerUsage(v2.TokenCounts{InputTokens: quantity(math.MaxInt64), OutputTokens: quantity(1)}, v3.TokenBreakdown{}, v2.Partial),
		"cache read exceeds input":          providerUsage(v2.TokenCounts{InputTokens: quantity(4)}, v3.TokenBreakdown{CachedInputTokens: quantity(5)}, v2.Partial),
		"cache write exceeds input":         providerUsage(v2.TokenCounts{InputTokens: quantity(4)}, v3.TokenBreakdown{CacheWriteInputTokens: quantity(5)}, v2.Partial),
		"cache total exceeds input":         providerUsage(v2.TokenCounts{InputTokens: quantity(4)}, v3.TokenBreakdown{CachedInputTokens: quantity(2), CacheWriteInputTokens: quantity(3)}, v2.Partial),
		"cache overflow without parent":     providerUsage(v2.TokenCounts{}, v3.TokenBreakdown{CachedInputTokens: quantity(math.MaxInt64), CacheWriteInputTokens: quantity(1)}, v2.Partial),
		"reasoning exceeds output":          providerUsage(v2.TokenCounts{OutputTokens: quantity(4)}, v3.TokenBreakdown{ReasoningOutputTokens: quantity(5)}, v2.Partial),
		"negative detail":                   providerUsage(v2.TokenCounts{}, v3.TokenBreakdown{ReasoningOutputTokens: quantity(-1)}, v2.Partial),
		"negative base":                     providerUsage(v2.TokenCounts{TotalTokens: quantity(-1)}, v3.TokenBreakdown{}, v2.Partial),
		"input exceeds partial total":       providerUsage(v2.TokenCounts{InputTokens: quantity(5), TotalTokens: quantity(4)}, v3.TokenBreakdown{}, v2.Partial),
		"output exceeds partial total":      providerUsage(v2.TokenCounts{OutputTokens: quantity(5), TotalTokens: quantity(4)}, v3.TokenBreakdown{}, v2.Partial),
		"cached exceeds total without input": providerUsage(v2.TokenCounts{TotalTokens: quantity(4)},
			v3.TokenBreakdown{CachedInputTokens: quantity(5)}, v2.Partial),
		"child sums exceed total without parents": providerUsage(v2.TokenCounts{TotalTokens: quantity(5)},
			v3.TokenBreakdown{CachedInputTokens: quantity(2), CacheWriteInputTokens: quantity(2), ReasoningOutputTokens: quantity(2)}, v2.Partial),
		"child sum overflow without parents": providerUsage(v2.TokenCounts{},
			v3.TokenBreakdown{CachedInputTokens: quantity(math.MaxInt64), ReasoningOutputTokens: quantity(1)}, v2.Partial),
	} {
		t.Run(name, func(t *testing.T) {
			if observation.Validate() == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
}
