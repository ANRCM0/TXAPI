package billing

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var ErrUnpricedDimension = errors.New("prepared request includes an unsupported billing dimension")

// ValidatePreparedRequest checks the actual text-only wire payload after
// conversion. It cannot prove implicit model reasoning or provider tariffs.
func ValidatePreparedRequest(body []byte, protocol string, embeddings bool) error {
	if protocol == "openai" {
		if _, err := ParseStrictRequest(body, embeddings); err != nil {
			return ErrUnpricedDimension
		}
		return nil
	}
	if embeddings || !utf8.Valid(body) || uniqueJSON(body) != nil {
		return ErrUnpricedDimension
	}
	fields, ok := wireObject(body)
	if !ok {
		return ErrUnpricedDimension
	}
	switch protocol {
	case "anthropic":
		if !wireKeys(fields, "model", "messages", "system", "max_tokens", "temperature", "top_p", "stop_sequences", "stream") {
			return ErrUnpricedDimension
		}
		if _, ok := strictString(fields["model"]); !ok {
			return ErrUnpricedDimension
		}
		if raw, exists := fields["system"]; exists {
			if _, ok := strictString(raw); !ok {
				return ErrUnpricedDimension
			}
		}
		messages, ok := wireArray(fields["messages"])
		if !ok {
			return ErrUnpricedDimension
		}
		for _, raw := range messages {
			message, ok := wireObject(raw)
			role, _ := strictString(message["role"])
			if !ok || !wireKeys(message, "role", "content") || (role != "user" && role != "assistant") {
				return ErrUnpricedDimension
			}
			blocks, ok := wireArray(message["content"])
			if !ok {
				return ErrUnpricedDimension
			}
			for _, raw := range blocks {
				block, ok := wireObject(raw)
				kind, _ := strictString(block["type"])
				if !ok || !wireKeys(block, "type", "text") || kind != "text" || !wireOptionalText(block, "text") {
					return ErrUnpricedDimension
				}
			}
		}
	case "gemini":
		if !wireKeys(fields, "contents", "systemInstruction", "generationConfig") {
			return ErrUnpricedDimension
		}
		contents, ok := wireArray(fields["contents"])
		if !ok {
			return ErrUnpricedDimension
		}
		if raw, exists := fields["systemInstruction"]; exists {
			contents = append(contents, raw)
		}
		for _, raw := range contents {
			content, ok := wireObject(raw)
			if !ok || !wireKeys(content, "role", "parts") || !wireOptionalText(content, "role") {
				return ErrUnpricedDimension
			}
			parts, ok := wireArray(content["parts"])
			if !ok {
				return ErrUnpricedDimension
			}
			for _, raw := range parts {
				part, ok := wireObject(raw)
				if !ok || !wireKeys(part, "text") || !wireOptionalText(part, "text") {
					return ErrUnpricedDimension
				}
			}
		}
		generation, ok := wireObject(fields["generationConfig"])
		if !ok || !wireKeys(generation, "maxOutputTokens", "candidateCount", "temperature", "topP", "stopSequences", "seed") {
			return ErrUnpricedDimension
		}
	default:
		return ErrUnpricedDimension
	}
	return nil
}

func wireObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	err := json.Unmarshal(raw, &fields)
	return fields, err == nil && fields != nil
}

func wireArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	err := json.Unmarshal(raw, &items)
	return items, err == nil && len(items) > 0
}

func wireKeys(fields map[string]json.RawMessage, allowed ...string) bool {
	for key := range fields {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func wireOptionalText(fields map[string]json.RawMessage, key string) bool {
	raw, exists := fields[key]
	if !exists {
		return true
	}
	_, ok := strictString(raw)
	return ok
}
