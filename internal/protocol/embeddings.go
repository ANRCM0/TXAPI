package protocol

import (
	"encoding/json"
	"errors"

	"minigateway/internal/core"
)

// Embeddings keeps vector/base64 data and extension fields native. Its response
// is not a Chat response, and it has no SSE success terminal to manufacture.
type embeddingsDecoder struct{}

func decodeEmbeddings(body []byte) ([]byte, *core.Usage, error) {
	var response struct {
		Usage *core.Usage `json:"usage"`
	}
	_ = json.Unmarshal(body, &response)
	return body, response.Usage, nil
}

func (embeddingsDecoder) Decode(body []byte) ([]byte, *core.Usage, error) {
	return decodeEmbeddings(body)
}

func (embeddingsDecoder) NewStream() core.StreamDecoder { return rejectedEmbeddingsStream{} }

type rejectedEmbeddingsStream struct{}

func (rejectedEmbeddingsStream) DecodeLine([]byte) ([]core.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}
func (rejectedEmbeddingsStream) End() ([]core.Frame, error) {
	return nil, errors.New("embeddings does not support streaming")
}
