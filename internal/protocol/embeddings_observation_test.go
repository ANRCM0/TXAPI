package protocol

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func TestEmbeddingsNativeUsageOwnsReportedCounts(t *testing.T) {
	decoder, ok := any(embeddingsDecoder{}).(v3.Decoder)
	if !ok {
		t.Fatal("embeddings has no native usage consumer")
	}
	count := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		name, usage string
		counts      v2.TokenCounts
		state       v2.Completeness
		financial   bool
	}{
		{"input only", `{"prompt_tokens":11,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), TotalTokens: count(11)}, v2.Partial, true},
		{"input only zero", `{"prompt_tokens":0,"total_tokens":0}`, v2.TokenCounts{InputTokens: count(0), TotalTokens: count(0)}, v2.Partial, true},
		{"explicit output zero", `{"prompt_tokens":11,"completion_tokens":0,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), OutputTokens: count(0), TotalTokens: count(11)}, v2.Complete, true},
		{"null output", `{"prompt_tokens":11,"completion_tokens":null,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), TotalTokens: count(11)}, v2.Partial, true},
		{"input missing", `{"total_tokens":11}`, v2.TokenCounts{TotalTokens: count(11)}, v2.Partial, false},
		{"total missing", `{"prompt_tokens":11}`, v2.TokenCounts{InputTokens: count(11)}, v2.Partial, false},
		{"output only zero", `{"completion_tokens":0}`, v2.TokenCounts{OutputTokens: count(0)}, v2.Partial, false},
		{"large exact integer", `{"prompt_tokens":9007199254740993,"total_tokens":9007199254740993}`, v2.TokenCounts{InputTokens: count(9007199254740993), TotalTokens: count(9007199254740993)}, v2.Partial, true},
		{"maximum exact integer", `{"prompt_tokens":9223372036854775807,"total_tokens":9223372036854775807}`, v2.TokenCounts{InputTokens: count(math.MaxInt64), TotalTokens: count(math.MaxInt64)}, v2.Partial, true},
		{"empty", `{}`, v2.TokenCounts{}, v2.Unknown, false},
		{"null", `null`, v2.TokenCounts{}, v2.Unknown, false},
		{"all null", `{"prompt_tokens":null,"completion_tokens":null,"total_tokens":null}`, v2.TokenCounts{}, v2.Unknown, false},
		{"case aliases", `{"Prompt_Tokens":0,"Total_Tokens":0}`, v2.TokenCounts{}, v2.Unknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(` {"object":"list","data":[{"embedding":"AQID"}],"usage":` + tc.usage + `,"extension":{"keep":true}} `)
			envelope, err := decoder.DecodeUsageEnvelope(body)
			if err != nil || !envelope.Execution.ProtocolTerminal || !bytes.Equal(envelope.Execution.Data, body) || envelope.Execution.Failed {
				t.Fatal("wire or terminal changed", envelope, err)
			}
			u, base := envelope.Usage, envelope.Execution.Usage
			if u == nil || base == nil || u.Validate() != nil || u.Completeness != tc.state || base.Completeness != tc.state || !reflect.DeepEqual(u.Counts, tc.counts) || !reflect.DeepEqual(base.Counts, tc.counts) || u.Breakdown != (v3.TokenBreakdown{}) {
				t.Fatal("native counts were lost or inferred", u, base)
			}
			profile, source := "embeddings_native_tokens_v1", v2.ProviderReported
			if tc.state == v2.Unknown {
				profile, source = "", v2.SourceUnknown
			}
			if u.Profile != profile || base.Profile != profile || u.Source != source || base.Source != source {
				t.Fatal("native provenance changed", u, base)
			}
			wire, financial, err := (embeddingsDecoder{}).Decode(body)
			if err != nil || !bytes.Equal(wire, body) || (financial != nil) != tc.financial {
				t.Fatal("legacy input-only accounting changed", financial, err)
			}
			if financial != nil && (int64(financial.PromptTokens) != *tc.counts.InputTokens || financial.CompletionTokens != 0 || int64(financial.TotalTokens) != *tc.counts.TotalTokens) {
				t.Fatal("legacy projection disagrees with known input and total", financial)
			}
		})
	}
}

func TestEmbeddingsNativeUsageRejectsInvalidCountsAndStreams(t *testing.T) {
	decoder, ok := any(embeddingsDecoder{}).(v3.Decoder)
	if !ok {
		t.Fatal("embeddings has no native usage consumer")
	}
	for _, body := range []string{
		`null`, `[]`, `{"error":{"message":"failed"}}`, `{"usage":[]}`,
		`{"usage":{"prompt_tokens":11,"total_tokens":12}}`,
		`{"usage":{"completion_tokens":1}}`,
		`{"usage":{"prompt_tokens":-1}}`,
		`{"usage":{"prompt_tokens":"11"}}`,
		`{"usage":{"prompt_tokens":11.0}}`,
		`{"usage":{"total_tokens":9223372036854775808}}`,
		`{"usage":{"total_tokens":0,"total_tokens":1}}`,
		`{"usage":{},"usage":null}`,
	} {
		if _, err := decoder.DecodeUsageEnvelope([]byte(body)); err == nil {
			t.Fatal("invalid native usage accepted", body)
		}
	}
	stream := decoder.NewUsageEnvelopeStream()
	if stream == nil {
		t.Fatal("missing explicit stream rejection")
	}
	if _, err := stream.DecodeUsageEnvelopes([]byte("data: [DONE]")); err == nil {
		t.Fatal("embeddings accepted streaming")
	}
	if _, err := stream.EndUsageEnvelopes(); err == nil {
		t.Fatal("embeddings manufactured a stream terminal")
	}
}
