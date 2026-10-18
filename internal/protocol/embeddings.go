package protocol

import (
	"encoding/json"
	"errors"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

// Embeddings keeps vector/base64 data and extension fields native. Its response
// is not a Chat response, and it has no SSE success terminal to manufacture.
type embeddingsDecoder struct{}

func decodeEmbeddings(body []byte) ([]byte, *execv1.Usage, error) {
	var response struct {
		Usage *execv1.Usage `json:"usage"`
	}
	_ = json.Unmarshal(body, &response)
	return body, response.Usage, nil
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
