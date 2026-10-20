package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	execv2 "minigateway/internal/foundation/contracts/execution/v2"
	execv3 "minigateway/internal/foundation/contracts/execution/v3"
	"minigateway/internal/protocol/jsonutil"
)

// Embeddings keeps vector/base64 data and extension fields native. Its response
// is not a Chat response, and it has no SSE success terminal to manufacture.
type embeddingsDecoder struct{}

func decodeEmbeddings(body []byte) ([]byte, *execv1.Usage, error) {
	usage, err := nativeEmbeddingsUsage(body)
	if err != nil {
		return nil, nil, err
	}
	counts := usage.Counts
	if counts.InputTokens == nil || counts.TotalTokens == nil {
		return body, nil, nil
	}
	// Preserve the existing input-only accounting projection. An absent output
	// count remains absent in native observations, including known-zero input.
	return body, &execv1.Usage{PromptTokens: int(*counts.InputTokens), TotalTokens: int(*counts.TotalTokens)}, nil
}

func nativeEmbeddingsUsage(body []byte) (*execv3.UsageObservation, error) {
	if err := jsonutil.Validate(body); err != nil {
		return nil, err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if response == nil || embeddingsPresent(response["error"]) {
		return nil, errors.New("invalid embeddings response")
	}
	usage := &execv3.UsageObservation{Completeness: execv2.Unknown, Source: execv2.SourceUnknown}
	if !embeddingsPresent(response["usage"]) {
		return usage, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response["usage"], &fields); err != nil {
		return nil, err
	}
	counts := [3]*int64{}
	for i, name := range []string{"prompt_tokens", "total_tokens", "completion_tokens"} {
		if !embeddingsPresent(fields[name]) {
			continue
		}
		var count int64
		if err := json.Unmarshal(fields[name], &count); err != nil {
			return nil, err
		}
		if count < 0 || count > math.MaxInt {
			return nil, errors.New("invalid embeddings token count")
		}
		counts[i] = &count
	}
	if counts[2] != nil && *counts[2] != 0 {
		return nil, errors.New("embeddings cannot report output tokens")
	}
	if counts[0] != nil && counts[1] != nil && *counts[0] != *counts[1] {
		return nil, errors.New("inconsistent embeddings token counts")
	}
	usage.Counts = execv2.TokenCounts{InputTokens: counts[0], OutputTokens: counts[2], TotalTokens: counts[1]}
	known := 0
	for _, count := range counts {
		if count != nil {
			known++
		}
	}
	if known != 0 {
		usage.Completeness, usage.Source, usage.Profile = execv2.Partial, execv2.ProviderReported, "embeddings_native_tokens_v1"
		if known == 3 {
			usage.Completeness = execv2.Complete
		}
	}
	return usage, usage.Validate()
}

func embeddingsPresent(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (embeddingsDecoder) Decode(body []byte) ([]byte, *execv1.Usage, error) {
	return decodeEmbeddings(body)
}

func (embeddingsDecoder) DecodeUsageEnvelope(body []byte) (execv3.Envelope, error) {
	usage, err := nativeEmbeddingsUsage(body)
	if err != nil {
		return execv3.Envelope{}, err
	}
	base := &execv2.UsageObservation{Counts: usage.Counts, Completeness: usage.Completeness, Source: usage.Source, Profile: usage.Profile}
	return execv3.Envelope{Execution: execv2.Envelope{Data: body, Usage: base, ProtocolTerminal: true}, Usage: usage}, nil
}

func (embeddingsDecoder) NewUsageEnvelopeStream() execv3.StreamDecoder {
	return rejectedEmbeddingsStream{}
}

func (embeddingsDecoder) NewStream() execv1.StreamDecoder { return rejectedEmbeddingsStream{} }

type rejectedEmbeddingsStream struct{}

func (rejectedEmbeddingsStream) DecodeLine([]byte) ([]execv1.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}
func (rejectedEmbeddingsStream) End() ([]execv1.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}

func (rejectedEmbeddingsStream) DecodeUsageEnvelopes([]byte) ([]execv3.Envelope, error) {
	return nil, errors.New("embeddings does not support streaming")
}

func (rejectedEmbeddingsStream) EndUsageEnvelopes() ([]execv3.Envelope, error) {
	return nil, errors.New("embeddings does not support streaming")
}

var _ execv3.Decoder = embeddingsDecoder{}
