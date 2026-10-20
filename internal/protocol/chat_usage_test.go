package protocol

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/adaptor/gemini"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
)

func chatUsageDecoder() v3.Decoder {
	return NewDecoder(&openai.Adaptor{}, &meta.Meta{}).(v3.Decoder)
}

func chatUsageEvent(t *testing.T, stream v3.StreamDecoder, event string) ([]v3.Envelope, error) {
	t.Helper()
	var out []v3.Envelope
	for _, line := range strings.Split(strings.TrimSuffix(event, "\n"), "\n") {
		frames, err := stream.DecodeUsageEnvelopes([]byte(line))
		if err != nil {
			return nil, err
		}
		out = append(out, frames...)
	}
	return out, nil
}

func chatUsageCounts(usage *v3.UsageObservation) [6]*int64 {
	return [6]*int64{usage.Counts.InputTokens, usage.Counts.OutputTokens, usage.Counts.TotalTokens,
		usage.Breakdown.CachedInputTokens, usage.Breakdown.CacheWriteInputTokens, usage.Breakdown.ReasoningOutputTokens}
}

func TestNativeChatUsageJSONAndSSE(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		state       v2.Completeness
		counts      [6]string
	}{
		{"omitted", "", v2.Unknown, [6]string{}},
		{"null", "null", v2.Unknown, [6]string{}},
		{"empty", `{}`, v2.Unknown, [6]string{}},
		{"null fields", `{"prompt_tokens":null,"completion_tokens_details":null}`, v2.Unknown, [6]string{}},
		{"complete", `{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}`, v2.Complete, [6]string{"10", "4", "14"}},
		{"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`, v2.Complete, [6]string{"0", "0", "0", "0", "", "0"}},
		{"partial no invented total", `{"prompt_tokens":10,"completion_tokens":4}`, v2.Partial, [6]string{"10", "4"}},
		{"total only", `{"total_tokens":14}`, v2.Partial, [6]string{"", "", "14"}},
		{"cache only", `{"prompt_tokens_details":{"cached_tokens":0}}`, v2.Partial, [6]string{"", "", "", "0"}},
		{"reasoning only", `{"completion_tokens_details":{"reasoning_tokens":3}}`, v2.Partial, [6]string{"", "", "", "", "", "3"}},
		{"complete details", `{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":9},"completion_tokens_details":{"reasoning_tokens":3}}`, v2.Complete, [6]string{"10", "4", "14", "2", "", "3"}},
		{"unknown aliases", `{"input_tokens":9,"output_tokens":4,"cached_tokens":8,"prompt_cache_hit_tokens":7,"input_tokens_details":{"cached_tokens":6},"PROMPT_TOKENS":5,"prompt_tokens_details":{"CACHED_TOKENS":4}}`, v2.Unknown, [6]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := ""
			if tc.usage != "" {
				field = `,"usage":` + tc.usage
			}
			body := `{"id":"native","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"extension":{"keep":true}` + field + `}`
			decoder := chatUsageDecoder()
			result, err := decoder.DecodeUsageEnvelope([]byte(body))
			if err != nil || !result.Execution.ProtocolTerminal || string(result.Execution.Data) != body {
				t.Fatalf("JSON delivery: %+v %v", result, err)
			}
			stream := decoder.NewUsageEnvelopeStream()
			wire := ": heartbeat\n\nevent: message\ndata: " + body + "\n\ndata: [DONE]\n\n"
			frames, err := chatUsageEvent(t, stream, wire)
			if err != nil {
				t.Fatal(err)
			}
			var encoded []byte
			terminals := 0
			for _, frame := range frames {
				encoded = append(encoded, frame.Execution.Data...)
				if frame.Execution.ProtocolTerminal {
					terminals++
					for _, snapshot := range []v3.Envelope{result, frame} {
						if snapshot.Usage == nil || snapshot.Usage.Completeness != tc.state {
							t.Fatalf("usage state: %+v", snapshot)
						}
						if snapshot.Usage.Source == v2.ProviderReported && snapshot.Usage.Profile != "chat_native_tokens_v1" {
							t.Fatal("wrong native profile", snapshot.Usage)
						}
						for i, count := range chatUsageCounts(snapshot.Usage) {
							if tc.counts[i] == "" && count != nil || tc.counts[i] != "" && (count == nil || strconv.FormatInt(*count, 10) != tc.counts[i]) {
								t.Fatalf("count %d=%v expected %q", i, count, tc.counts[i])
							}
						}
					}
				}
			}
			if !bytes.Equal(encoded, []byte(wire)) || terminals != 1 {
				t.Fatalf("wire/terminal mismatch: %q terminals=%d", encoded, terminals)
			}
			if tail, err := stream.EndUsageEnvelopes(); err != nil || len(tail) != 0 {
				t.Fatalf("End repeated terminal: %v %v", tail, err)
			}
			if _, err := stream.DecodeUsageEnvelopes([]byte(": late")); err == nil {
				t.Fatal("accepted event after terminal")
			}
		})
	}
}

func TestNativeChatUsageRejectsInvalidSnapshotsBeforeDelivery(t *testing.T) {
	for name, usage := range map[string]string{
		"negative":                 `{"prompt_tokens":-1}`,
		"float":                    `{"completion_tokens":1.5}`,
		"string":                   `{"total_tokens":"2"}`,
		"boolean":                  `{"total_tokens":true}`,
		"overflow":                 `{"total_tokens":9223372036854775808}`,
		"overflow sum":             `{"prompt_tokens":9223372036854775807,"completion_tokens":1}`,
		"inconsistent total":       `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":4}`,
		"cache exceeds input":      `{"prompt_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}`,
		"reasoning exceeds output": `{"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":2}}`,
		"children exceed total":    `{"total_tokens":3,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":2}}`,
		"negative cache":           `{"prompt_tokens_details":{"cached_tokens":-1}}`,
		"details array":            `{"prompt_tokens_details":[]}`,
		"details scalar":           `{"completion_tokens_details":1}`,
		"usage array":              `[]`,
		"duplicate count":          `{"prompt_tokens":1,"prompt_tokens":2}`,
		"duplicate detail":         `{"prompt_tokens_details":{"cached_tokens":0,"cached_tokens":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"choices":[{"finish_reason":"stop"}],"usage":` + usage + `}`
			if _, err := chatUsageDecoder().DecodeUsageEnvelope([]byte(body)); err == nil {
				t.Fatal("JSON accepted invalid usage")
			}
			frames, err := chatUsageEvent(t, chatUsageDecoder().NewUsageEnvelopeStream(), "data: "+body+"\n\n")
			if err == nil || len(frames) != 0 {
				t.Fatalf("SSE delivered invalid usage: %+v %v", frames, err)
			}
		})
	}
	for _, body := range []string{"null", "[]", `{"error":{"message":"bad"}}`, `{"usage":{},"usage":null}`} {
		if _, err := chatUsageDecoder().DecodeUsageEnvelope([]byte(body)); err == nil {
			t.Fatal("accepted invalid response", body)
		}
	}
}

func TestNativeChatUsageSnapshotLifetimeProgressAndIsolation(t *testing.T) {
	decoder := chatUsageDecoder()
	first, second := decoder.NewUsageEnvelopeStream(), decoder.NewUsageEnvelopeStream()
	for _, stream := range []v3.StreamDecoder{first, second} {
		if _, err := chatUsageEvent(t, stream, ": heartbeat\n\n"); err != nil {
			t.Fatal(err)
		}
		if progress := stream.(v4.StreamProgress).Progress(); progress.CompleteEvents != 1 || progress.EffectiveOutput {
			t.Fatal("heartbeat became effective output", progress)
		}
	}
	known := `data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"lookup","arguments":"{\"city\":\"雪\"}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}` + "\n\n"
	if _, err := chatUsageEvent(t, first, known); err != nil {
		t.Fatal(err)
	}
	if !first.(v4.StreamProgress).Progress().EffectiveOutput || second.(v4.StreamProgress).Progress().EffectiveOutput {
		t.Fatal("tool progress leaked between streams")
	}
	finish := "data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	for index, stream := range []v3.StreamDecoder{first, second} {
		frames, err := chatUsageEvent(t, stream, finish)
		if err != nil || len(frames) != 2 {
			t.Fatalf("finish %v %v", frames, err)
		}
		usage := frames[1].Usage
		if index == 0 && (usage.Completeness != v2.Complete || *usage.Counts.TotalTokens != 4) || index == 1 && usage.Completeness != v2.Unknown {
			t.Fatal("missing frame erased latest snapshot or shared stream usage", index, usage)
		}
	}
	reset := decoder.NewUsageEnvelopeStream()
	if _, err := chatUsageEvent(t, reset, known+"data: {\"usage\":null}\n\n"); err != nil {
		t.Fatal(err)
	}
	frames, err := chatUsageEvent(t, reset, finish)
	if err != nil || frames[1].Usage.Completeness != v2.Unknown {
		t.Fatal("explicit null retained old counts", frames, err)
	}
	truncated := decoder.NewUsageEnvelopeStream()
	if _, err := chatUsageEvent(t, truncated, known); err != nil {
		t.Fatal(err)
	}
	if _, err := truncated.EndUsageEnvelopes(); err == nil {
		t.Fatal("usage manufactured terminal completion")
	}
}

func TestNativeChatUsageOptionalPortDoesNotChangeConvertedOrLegacyDecoders(t *testing.T) {
	for _, decoder := range []v1.Decoder{NewDecoder(&anthropic.Adaptor{}, &meta.Meta{}), NewDecoder(&gemini.Adaptor{}, &meta.Meta{})} {
		if _, rich := decoder.(v3.Decoder); rich {
			t.Fatal("unrelated decoder acquired native Chat semantics")
		}
	}
	embeddings := NewDecoder(&openai.Adaptor{}, &meta.Meta{Mode: meta.ModeEmbeddings}).(v3.Decoder)
	observation, err := embeddings.DecodeUsageEnvelope([]byte(`{"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	if err != nil || observation.Usage.Profile != "embeddings_native_tokens_v1" || observation.Usage.Counts.OutputTokens != nil {
		t.Fatal("embeddings acquired native Chat provenance or inferred output", observation, err)
	}
	body := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":"invalid"}}}`)
	legacy := NewDecoder(&openai.Adaptor{}, &meta.Meta{})
	if wire, usage, err := legacy.Decode(body); err != nil || usage == nil || usage.TotalTokens != 2 || !bytes.Equal(wire, body) {
		t.Fatal("v1 projection changed", usage, err)
	}
	if _, err := legacy.(v3.Decoder).DecodeUsageEnvelope(body); err == nil {
		t.Fatal("native observation accepted invalid detail")
	}
}
