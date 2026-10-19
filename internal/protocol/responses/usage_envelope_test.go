package responses

import (
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func TestUsageEnvelopeNativeSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		state       v2.Completeness
		invalid     bool
	}{
		{"missing", `null`, v2.Unknown, false},
		{"empty", `{}`, v2.Unknown, false},
		{"null details", `{"input_tokens_details":null,"output_tokens_details":{"reasoning_tokens":null}}`, v2.Unknown, false},
		{"breakdown only", `{"input_tokens_details":{"cached_tokens":0}}`, v2.Partial, false},
		{"complete missing details", `{"input_tokens":3,"output_tokens":2,"total_tokens":5}`, v2.Complete, false},
		{"native zero beats aliases", `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"cached_tokens":9,"cache_creation_input_tokens":9,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, v2.Complete, false},
		{"unknown casing", `{"input_tokens_details":{"CACHED_TOKENS":7},"output_tokens_details":{"REASONING_TOKENS":3}}`, v2.Unknown, false},
		{"unknown aliases", `{"cached_tokens":7,"input_tokens_details":{"cache_read_tokens":7}}`, v2.Unknown, false},
		{"negative", `{"input_tokens_details":{"cached_tokens":-1}}`, "", true},
		{"string", `{"output_tokens_details":{"reasoning_tokens":"2"}}`, "", true},
		{"fraction", `{"input_tokens_details":{"cache_write_tokens":1.5}}`, "", true},
		{"bool", `{"input_tokens_details":{"cached_tokens":true}}`, "", true},
		{"overflow integer", `{"output_tokens_details":{"reasoning_tokens":9223372036854775808}}`, "", true},
		{"overflow sum", `{"input_tokens_details":{"cached_tokens":9223372036854775807,"cache_write_tokens":1}}`, "", true},
		{"overflow partial bases", `{"input_tokens":9223372036854775807,"output_tokens":1}`, "", true},
		{"cache parent", `{"input_tokens":1,"input_tokens_details":{"cached_tokens":2}}`, "", true},
		{"cache sum parent", `{"input_tokens":3,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":2}}`, "", true},
		{"reasoning parent", `{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`, "", true},
		{"details array", `{"input_tokens_details":[]}`, "", true},
		{"duplicate details", `{"input_tokens_details":{"cached_tokens":0,"cached_tokens":1}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"native","object":"response","status":"completed","usage":` + tc.usage + `}`
			out, err := NewDecoder().DecodeUsageEnvelope([]byte(body))
			if (err != nil) != tc.invalid {
				t.Fatalf("JSON error=%v want invalid=%v", err, tc.invalid)
			}
			stream := NewDecoder().NewUsageEnvelopeStream()
			event := `{"type":"response.completed","response":` + body + `}`
			if frames, err := stream.DecodeUsageEnvelopes([]byte("data: " + event)); err != nil || len(frames) != 0 {
				t.Fatalf("buffer: %v %v", frames, err)
			}
			frames, streamErr := stream.DecodeUsageEnvelopes(nil)
			if (streamErr != nil) != tc.invalid {
				t.Fatalf("SSE error=%v want invalid=%v", streamErr, tc.invalid)
			}
			if tc.invalid {
				return
			}
			if len(frames) != 1 || strings.Count(string(frames[0].Execution.Data), "data:") != 1 {
				t.Fatalf("single delivery: %+v", frames)
			}
			if tail, err := stream.EndUsageEnvelopes(); err != nil || len(tail) != 0 {
				t.Fatalf("terminal repeated: %v %v", tail, err)
			}
			for _, envelope := range []v3.Envelope{out, frames[0]} {
				if envelope.Usage == nil || envelope.Usage.Completeness != tc.state {
					t.Fatalf("state: %+v", envelope.Usage)
				}
				if envelope.Usage.Source == v2.ProviderReported && envelope.Usage.Profile != "responses_native_tokens_v2" {
					t.Fatal("wrong enriched profile")
				}
				if tc.state == v2.Unknown && (envelope.Usage.Source != v2.SourceUnknown || envelope.Usage.Profile != "") {
					t.Fatal("unknown provenance")
				}
				if tc.name == "breakdown only" && (envelope.Execution.Usage.Completeness != v2.Unknown || envelope.Usage.Breakdown.CachedInputTokens == nil || *envelope.Usage.Breakdown.CachedInputTokens != 0) {
					t.Fatal("breakdown-only projection lost")
				}
				if tc.name == "native zero beats aliases" {
					for _, n := range []*int64{envelope.Usage.Breakdown.CachedInputTokens, envelope.Usage.Breakdown.CacheWriteInputTokens, envelope.Usage.Breakdown.ReasoningOutputTokens} {
						if n == nil || *n != 0 {
							t.Fatal("explicit zero lost")
						}
					}
				}
			}
			if string(out.Execution.Data) != body {
				t.Fatal("JSON delivery changed")
			}
		})
	}
}

func TestUsageEnvelopeLegacyDetailsAndAttemptIsolation(t *testing.T) {
	body := []byte(`{"id":"native","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":"invalid"}}}`)
	if _, usage, err := NewDecoder().Decode(body); err != nil || usage == nil {
		t.Fatalf("legacy v1 rejected details: %v", err)
	}
	if out, err := NewDecoder().DecodeEnvelope(body); err != nil || out.Usage.Profile != "responses_native_tokens_v1" {
		t.Fatalf("legacy v2 changed: %+v %v", out, err)
	}
	if _, err := NewDecoder().DecodeUsageEnvelope(body); err == nil {
		t.Fatal("v3 accepted invalid details")
	}
	event := []byte(`data: {"type":"response.completed","response":` + string(body) + `}`)
	legacy := NewDecoder().NewStream()
	if _, err := legacy.DecodeLine(event); err != nil {
		t.Fatal(err)
	}
	if frames, err := legacy.DecodeLine(nil); err != nil || len(frames) != 1 || frames[0].Usage == nil {
		t.Fatalf("legacy v1 stream changed: %+v %v", frames, err)
	}
	legacyFacts := NewDecoder().NewEnvelopeStream()
	if _, err := legacyFacts.DecodeEnvelopes(event); err != nil {
		t.Fatal(err)
	}
	if frames, err := legacyFacts.DecodeEnvelopes(nil); err != nil || len(frames) != 1 || frames[0].Usage.Profile != "responses_native_tokens_v1" {
		t.Fatalf("legacy v2 stream changed: %+v %v", frames, err)
	}
	d := NewDecoder()
	first := d.NewUsageEnvelopeStream()
	line := `data: {"type":"response.created","response":{"id":"a","object":"response","status":"in_progress","usage":{"input_tokens_details":{"cached_tokens":4}}}}`
	first.DecodeUsageEnvelopes([]byte(line))
	frames, err := first.DecodeUsageEnvelopes(nil)
	if err != nil || len(frames) != 1 || frames[0].Usage.Breakdown.CachedInputTokens == nil {
		t.Fatalf("partial: %+v %v", frames, err)
	}
	second := d.NewUsageEnvelopeStream()
	second.DecodeUsageEnvelopes([]byte(": comment"))
	other, err := second.DecodeUsageEnvelopes(nil)
	if err != nil || len(other) != 1 || other[0].Usage != nil {
		t.Fatal("usage leaked into another attempt")
	}
	first.DecodeUsageEnvelopes([]byte(`data: {"type":"response.completed","response":{"id":"a","object":"response","status":"completed","usage":{"input_tokens":4,"output_tokens":0,"total_tokens":4}}}`))
	final, err := first.DecodeUsageEnvelopes(nil)
	if err != nil || len(final) != 1 || final[0].Usage.Breakdown.CachedInputTokens != nil {
		t.Fatal("stale detail carried into terminal")
	}
	if *frames[0].Usage.Breakdown.CachedInputTokens != 4 {
		t.Fatal("later event mutated old snapshot")
	}
}
