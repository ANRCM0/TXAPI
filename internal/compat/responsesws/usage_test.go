package responsesws

import (
	"bytes"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"testing"
)

func TestDecodeUsagePreservesNativeDimensionsWithoutExtraFrames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   any
		invalid bool
	}{
		{"exact", map[string]any{"input_tokens": int64(9007199254740993), "output_tokens": int64(0), "total_tokens": int64(9007199254740993), "input_tokens_details": map[string]int{"cached_tokens": 1, "cache_write_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}, false},
		{"partial", map[string]any{"output_tokens": 2, "output_tokens_details": map[string]int{"reasoning_tokens": 1}}, false},
		{"unknown", nil, false},
		{"negative", map[string]any{"input_tokens": 2, "input_tokens_details": map[string]int{"cached_tokens": -1}}, true},
		{"excess", map[string]any{"output_tokens": 2, "output_tokens_details": map[string]int{"reasoning_tokens": 3}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDecoder("m")
			if _, err := d.DecodeUsage(event(t, "response.created", "in_progress", nil)); err != nil {
				t.Fatal(err)
			}
			frame := event(t, "response.completed", "completed", tc.usage)
			out, err := d.DecodeUsage(frame)
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v: %+v %v", tc.invalid, out, err)
			}
			if err != nil {
				return
			}
			if !out.Execution.ProtocolTerminal || !bytes.Equal(frame.Data, out.Execution.Data) {
				t.Fatal("changed native frame")
			}
			if tc.name == "exact" {
				if out.Usage == nil || out.Usage.Counts.TotalTokens == nil || *out.Usage.Counts.TotalTokens != 9007199254740993 || out.Usage.Breakdown.CacheWriteInputTokens == nil || *out.Usage.Breakdown.CacheWriteInputTokens != 0 || out.Execution.Usage == nil || *out.Execution.Usage.Counts.TotalTokens != 9007199254740993 {
					t.Fatalf("lost precision or known zero: %+v", out)
				}
			}
			if tc.name == "partial" && (out.Usage == nil || out.Usage.Completeness != v2.Partial || out.Usage.Counts.TotalTokens != nil || out.Usage.Breakdown.ReasoningOutputTokens == nil) {
				t.Fatalf("lost partial: %+v", out)
			}
			if _, err := d.DecodeUsage(frame); err == nil {
				t.Fatal("accepted duplicate terminal")
			}
		})
	}
}
