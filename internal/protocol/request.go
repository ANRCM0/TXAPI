package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/responses"
)

// Payload projections are operation-specific; unmodeled fields stay in Body.
// These are not canonical Chat conversions or strict input-bound proofs.
type ChatRequest struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens"`
	Messages  json.RawMessage `json:"messages"`
	Tools     json.RawMessage `json:"tools"`
}

type EmbeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat json.RawMessage `json:"encoding_format"`
	Dimensions     json.RawMessage `json:"dimensions"`
}

// Request owns an immutable body and its matching routing/capability facts.
// Each attempt receives a copy, so a converter cannot mutate later retries.
type Request struct {
	operation    Operation
	body         []byte
	chat         ChatRequest
	embeddings   EmbeddingsRequest
	responses    responses.Request
	requirements Requirements
}

func ParseRequest(operation Operation, body []byte) (Request, error) {
	r := Request{operation: operation, body: bytes.Clone(body)}
	if len(body) == 0 {
		return Request{}, errors.New("request body is empty")
	}
	switch operation {
	case Responses:
		var err error
		r.responses, err = responses.ParseRequest(r.body)
		if err != nil {
			return Request{}, err
		}
		if err := r.responses.ValidateHTTP(); err != nil {
			return Request{}, err
		}
	case Chat:
		if err := json.Unmarshal(r.body, &r.chat); err != nil {
			return Request{}, fmt.Errorf("invalid chat request JSON: %w", err)
		}
	case Embeddings:
		if err := json.Unmarshal(r.body, &r.embeddings); err != nil {
			return Request{}, fmt.Errorf("invalid embeddings request JSON: %w", err)
		}
		// Keep routing type checks without treating input as Chat messages.
		var flags struct {
			Stream    bool `json:"stream"`
			MaxTokens int  `json:"max_tokens"`
		}
		if err := json.Unmarshal(r.body, &flags); err != nil {
			return Request{}, fmt.Errorf("invalid embeddings routing fields: %w", err)
		}
	default:
		return Request{}, errors.New("unsupported operation")
	}
	if r.Model() == "" {
		return Request{}, errors.New("model is required")
	}
	r.requirements = RequirementsFor(operation, r.body)
	return r, nil
}

func (r Request) Operation() Operation       { return r.operation }
func (r Request) Body() []byte               { return bytes.Clone(r.body) }
func (r Request) Requirements() Requirements { return r.requirements }
func (r Request) Stream() bool               { return r.requirements.Stream }
func (r Request) Model() string {
	if r.operation == Responses {
		return r.responses.Model
	}
	if r.operation == Embeddings {
		return r.embeddings.Model
	}
	return r.chat.Model
}

// LegacyMaxTokens projects the native output limit for legacy estimates, never
// a strict cost proof. Embeddings has no output-token limit.
func (r Request) LegacyMaxTokens() int {
	if r.operation == Responses {
		return r.responses.MaxOutputTokens()
	}
	return r.chat.MaxTokens
}

func (r Request) Responses() (responses.Request, bool) {
	if r.operation != Responses {
		return responses.Request{}, false
	}
	native, _ := responses.ParseRequest(r.body)
	return native, true
}

func (r Request) Chat() (ChatRequest, bool) {
	chat := r.chat
	chat.Messages, chat.Tools = bytes.Clone(chat.Messages), bytes.Clone(chat.Tools)
	return chat, r.operation == Chat
}

func (r Request) Embeddings() (EmbeddingsRequest, bool) {
	embeddings := r.embeddings
	embeddings.Input = bytes.Clone(embeddings.Input)
	embeddings.EncodingFormat = bytes.Clone(embeddings.EncodingFormat)
	embeddings.Dimensions = bytes.Clone(embeddings.Dimensions)
	return embeddings, r.operation == Embeddings
}

func modeForOperation(operation Operation) (int, bool) {
	switch operation {
	case Responses:
		return meta.ModeResponses, true
	case Chat:
		return meta.ModeChatCompletions, true
	case Embeddings:
		return meta.ModeEmbeddings, true
	default:
		return 0, false
	}
}

// OperationForMode is only for the old HTTP/adaptor compatibility boundary.
// Mode zero historically meant Chat; unknown modes must never fall back to it.
func OperationForMode(mode int) (Operation, bool) {
	switch mode {
	case meta.ModeResponses:
		return Responses, true
	case 0, meta.ModeChatCompletions:
		return Chat, true
	case meta.ModeEmbeddings:
		return Embeddings, true
	default:
		return "", false
	}
}
