package adaptor

import execution "minigateway/internal/foundation/contracts/execution/v1"

// Operation describes semantics, not an HTTP path or a billing permission.
type Operation = execution.Operation

const (
	Chat       Operation = "chat"
	Embeddings Operation = "embeddings"
	Responses  Operation = "responses"
)

type Transport = execution.Transport

const (
	HTTP       = execution.HTTP
	Background = execution.Background
	WebSocket  = execution.WebSocket
)

type Requirements = execution.Requirements

// Capability is the typed, operation-specific opt-in consumed by protocol.
// It does not authorize a model, price, credential or strict execution.
// Embeddings currently promises native OpenAI-compatible payloads and usage;
// the Chat codec is never a fallback for a different embeddings wire format.
type Capability interface {
	Supports(Requirements) bool
}
