// SPDX-License-Identifier: Apache-2.0
// Checked token-bound arithmetic adapted from codex-proxy-rs, events.rs,
// billable_usage_is_complete, commit 591f95cc18ce95eb6fc87cf46a3526c5b3ecba36.
// Modified for optional nonnegative int64 counts, explicit provenance and
// completeness, unknown totals, and reasoning-output bounds. See THIRD_PARTY_NOTICES.md.
package execution

import (
	"errors"
	"math"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

// Validate checks quantity consistency, not billing permission or delivery.
func (u UsageObservation) Validate() error {
	base := []*int64{u.Counts.InputTokens, u.Counts.OutputTokens, u.Counts.TotalTokens}
	details := []*int64{u.Breakdown.CachedInputTokens, u.Breakdown.CacheWriteInputTokens, u.Breakdown.ReasoningOutputTokens}
	known := 0
	for _, counts := range [][]*int64{base, details} {
		for _, count := range counts {
			if count != nil {
				if *count < 0 {
					return errors.New("negative token observation")
				}
				known++
			}
		}
	}
	baseKnown := 0
	for _, count := range base {
		if count != nil {
			baseKnown++
		}
	}
	if known == 0 {
		if u.Completeness == v2.Unknown && u.Source == v2.SourceUnknown && u.Profile == "" {
			return nil
		}
		return errors.New("unknown token observation has invalid provenance")
	}
	if u.Source != v2.ProviderReported || !validProfile(u.Profile) {
		return errors.New("token observation requires a declared provider profile")
	}
	if baseKnown == 3 && u.Completeness != v2.Complete || baseKnown < 3 && u.Completeness != v2.Partial {
		return errors.New("token completeness does not match known quantities")
	}
	input, output, total := u.Counts.InputTokens, u.Counts.OutputTokens, u.Counts.TotalTokens
	if input != nil && output != nil {
		if *input > math.MaxInt64-*output {
			return errors.New("token total overflows")
		}
		if total != nil && *input+*output != *total {
			return errors.New("token total is inconsistent")
		}
	}
	cached, written := u.Breakdown.CachedInputTokens, u.Breakdown.CacheWriteInputTokens
	for _, child := range []*int64{cached, written} {
		if input != nil && child != nil && *child > *input {
			return errors.New("cache quantity exceeds input")
		}
	}
	if cached != nil && written != nil {
		if *cached > math.MaxInt64-*written {
			return errors.New("cache total overflows")
		}
		if input != nil && *cached+*written > *input {
			return errors.New("cache total exceeds input")
		}
	}
	if reasoning := u.Breakdown.ReasoningOutputTokens; reasoning != nil && output != nil && *reasoning > *output {
		return errors.New("reasoning quantity exceeds output")
	}
	// Unknown parents still have lower bounds from their known children. These
	// bounds reject impossible totals without turning an unknown into a count.
	var inputFloor, outputFloor int64
	for _, child := range []*int64{cached, written} {
		if child != nil {
			inputFloor += *child // The cache sum was checked above.
		}
	}
	if input != nil {
		inputFloor = *input
	}
	if reasoning := u.Breakdown.ReasoningOutputTokens; reasoning != nil {
		outputFloor = *reasoning
	}
	if output != nil {
		outputFloor = *output
	}
	if inputFloor > math.MaxInt64-outputFloor {
		return errors.New("token lower bound overflows")
	}
	if total != nil && inputFloor+outputFloor > *total {
		return errors.New("token lower bound exceeds total")
	}
	return nil
}

func validProfile(profile string) bool {
	if len(profile) == 0 || len(profile) > 64 {
		return false
	}
	for _, c := range []byte(profile) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}
