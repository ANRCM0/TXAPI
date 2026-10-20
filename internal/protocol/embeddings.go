package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol/jsonutil"
)

// Embeddings keeps vector/base64 data and extension fields native. Its response
// is not a Chat response, and it has no SSE success terminal to manufacture.
type embeddingsDecoder struct{}

func decodeEmbeddings(body []byte) ([]byte, *execv1.Usage, error) {
	if err := jsonutil.Validate(body); err != nil {
		return nil, nil, err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, nil, err
	}
	if response == nil || embeddingsPresent(response["error"]) {
		return nil, nil, errors.New("invalid embeddings response")
	}
	if !embeddingsPresent(response["usage"]) {
		return body, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response["usage"], &fields); err != nil {
		return nil, nil, err
	}
	counts := [3]*int64{}
	for i, name := range []string{"prompt_tokens", "total_tokens", "completion_tokens"} {
		if !embeddingsPresent(fields[name]) {
			continue
		}
		var count int64
		if err := json.Unmarshal(fields[name], &count); err != nil {
			return nil, nil, err
		}
		if count < 0 || count > math.MaxInt {
			return nil, nil, errors.New("invalid embeddings token count")
		}
		counts[i] = &count
	}
	if counts[2] != nil && *counts[2] != 0 {
		return nil, nil, errors.New("embeddings cannot report output tokens")
	}
	if counts[0] == nil || counts[1] == nil {
		return body, nil, nil
	}
	if *counts[0] != *counts[1] {
		return nil, nil, errors.New("inconsistent embeddings token counts")
	}
	// This is the existing input-only financial projection, not native output
	// provenance. Missing input or total must never become a zero-cost receipt.
	return body, &execv1.Usage{PromptTokens: int(*counts[0]), TotalTokens: int(*counts[1])}, nil
}

func embeddingsPresent(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (embeddingsDecoder) Decode(body []byte) ([]byte, *execv1.Usage, error) {
	return decodeEmbeddings(body)
}

func (embeddingsDecoder) NewStream() execv1.StreamDecoder { return rejectedEmbeddingsStream{} }

type rejectedEmbeddingsStream struct{}

func (rejectedEmbeddingsStream) DecodeLine([]byte) ([]execv1.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}
func (rejectedEmbeddingsStream) End() ([]execv1.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}
