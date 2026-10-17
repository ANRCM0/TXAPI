package protocol

import (
	"encoding/json"

	"minigateway/internal/protocol/adaptor"
)

type Operation = adaptor.Operation
type Transport = adaptor.Transport
type Requirements = adaptor.Requirements

const (
	Chat       = adaptor.Chat
	Embeddings = adaptor.Embeddings
	Responses  = adaptor.Responses
	HTTP       = adaptor.HTTP
	Background = adaptor.Background
	WebSocket  = adaptor.WebSocket
)

// RequirementsFor never rebuilds payloads. Malformed tool/history shapes are
// conservatively tool-bearing, not silently routed as a text-only capability.
func RequirementsFor(operation Operation, body []byte) Requirements {
	var request struct {
		Stream   bool            `json:"stream"`
		Tools    json.RawMessage `json:"tools"`
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(body, &request)
	r := Requirements{Operation: operation, Source: "openai", Transport: HTTP, Stream: request.Stream, Tools: nonempty(request.Tools)}
	if operation == Responses && nonempty(request.Input) {
		var text string
		if json.Unmarshal(request.Input, &text) != nil {
			var items []struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(request.Input, &items) != nil {
				r.Tools = true
			}
			for _, item := range items {
				r.Tools = r.Tools || item.Type == "function_call" || item.Type == "function_call_output"
			}
		}
	}
	if nonempty(request.Messages) {
		var messages []struct {
			Role  string          `json:"role"`
			Calls json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(request.Messages, &messages) != nil {
			r.Tools = true
		}
		for _, message := range messages {
			r.Tools = r.Tools || message.Role == "tool" || nonempty(message.Calls)
		}
	}
	return r
}

func nonempty(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var list []json.RawMessage
	return json.Unmarshal(raw, &list) != nil || len(list) > 0
}

// Supports applies to actual operation semantics, independently of model authorization.
// Existing third-party adaptors retain their Chat path. Only implemented HTTP
// operations may opt in; a declaration cannot invent Responses/worker/WS support.
func Supports(a adaptor.Adaptor, r Requirements) bool {
	if a == nil || (r.Source != "" && r.Source != "openai") {
		return false
	}
	if r.Transport != "" && r.Transport != HTTP {
		return false
	}
	if r.Operation != Chat && r.Operation != Embeddings && r.Operation != Responses {
		return false
	}
	if r.Operation == Embeddings && (r.Stream || r.Tools) {
		return false
	}
	if declaration, ok := a.(adaptor.Capability); ok {
		if r.Operation == Responses {
			native, ok := a.(adaptor.ProtocolAdaptor)
			if !ok || native.Name() != "openai" {
				return false
			}
		}
		return declaration.Supports(r)
	}
	if r.Operation == Responses {
		return false
	}
	// Existing extension declarations remain compatible; built-ins use Capability.
	if declaration, ok := a.(interface{ SupportsOperation(string, bool, bool) bool }); ok {
		return declaration.SupportsOperation(string(r.Operation), r.Stream, r.Tools)
	}
	return r.Operation == Chat && !r.Tools
}
