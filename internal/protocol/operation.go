package protocol

import (
	"encoding/json"
	"minigateway/internal/protocol/adaptor"
)

// Operation identifies semantics independently from the HTTP route or transport.
type Operation string

const (
	Chat       Operation = "chat"
	Embeddings Operation = "embeddings"
	Responses  Operation = "responses"
)

type Requirements struct {
	Operation Operation
	Source    string
	Transport Transport
	Stream    bool
	Tools     bool
}

func RequirementsFor(operation Operation, body []byte) Requirements {
	var request struct {
		Stream   bool              `json:"stream"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role  string            `json:"role"`
			Calls []json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &request)
	r := Requirements{Operation: operation, Source: "openai", Transport: HTTP, Stream: request.Stream, Tools: len(request.Tools) > 0}
	for _, message := range request.Messages {
		r.Tools = r.Tools || message.Role == "tool" || len(message.Calls) > 0
	}
	return r
}

type Transport string

const (
	HTTP       Transport = "http"
	Background Transport = "background"
	WebSocket  Transport = "websocket"
)

// Supports applies to actual operation semantics, independently of model authorization.
// Existing third-party adaptors retain their Chat path; new operations opt in.
func Supports(a adaptor.Adaptor, r Requirements) bool {
	if r.Source != "" && r.Source != "openai" {
		return false
	}
	if r.Transport != "" && r.Transport != HTTP {
		return false
	}
	if declaration, ok := a.(interface{ SupportsOperation(string, bool, bool) bool }); ok {
		return declaration.SupportsOperation(string(r.Operation), r.Stream, r.Tools)
	}
	return r.Operation == Chat && !r.Tools
}
