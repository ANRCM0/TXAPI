package billing

import (
	"bytes"
	"encoding/json"
	"errors"
)

// The initial strict tariff only prices text token dimensions. Paid tools,
// media and unknown extensions must not silently enter token-only accounting.
func ValidateStrictRequest(body []byte, embeddings bool) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{"model": true, "user": true}
	if embeddings {
		for _, field := range []string{"input", "dimensions", "encoding_format"} {
			allowed[field] = true
		}
		var single string
		var batch []string
		if bytes.Equal(bytes.TrimSpace(fields["input"]), []byte("null")) ||
			(json.Unmarshal(fields["input"], &single) != nil && json.Unmarshal(fields["input"], &batch) != nil) {
			return errors.New("strict embeddings require text input")
		}
	} else {
		for _, field := range []string{"messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "presence_penalty", "frequency_penalty", "seed", "n"} {
			allowed[field] = true
		}
		var messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		// Unknown per-message fields may carry tool/media charges.
		var rawMessages []map[string]json.RawMessage
		if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 ||
			json.Unmarshal(fields["messages"], &rawMessages) != nil {
			return errors.New("strict chat requires text-only messages")
		}
		for i, message := range rawMessages {
			for key := range message {
				if key != "role" && key != "content" && key != "name" {
					return errors.New("strict chat does not support tool or media message fields")
				}
			}
			switch messages[i].Role {
			case "user", "assistant", "system", "developer":
			default:
				return errors.New("unsupported strict message role")
			}
		}
		max, alternate := 0, 0
		if raw, ok := fields["max_tokens"]; ok {
			if json.Unmarshal(raw, &max) != nil || max <= 0 {
				return errors.New("strict max_tokens must be positive")
			}
		}
		if raw, ok := fields["max_completion_tokens"]; ok {
			if json.Unmarshal(raw, &alternate) != nil || alternate <= 0 {
				return errors.New("strict max_completion_tokens must be positive")
			}
		}
		if (max == 0) == (alternate == 0) {
			return errors.New("strict chat requires exactly one explicit output token limit")
		}
		if raw, ok := fields["n"]; ok {
			var n int
			if json.Unmarshal(raw, &n) != nil || n != 1 {
				return errors.New("strict chat supports n=1 only")
			}
		}
		if raw, ok := fields["stream_options"]; ok {
			var options map[string]json.RawMessage
			if json.Unmarshal(raw, &options) != nil {
				return errors.New("invalid strict stream options")
			}
			for key := range options {
				if key != "include_usage" {
					return errors.New("unsupported strict stream option")
				}
			}
		}
	}
	for field := range fields {
		if !allowed[field] {
			return errors.New("request includes an unpriced strict billing dimension or unsupported field")
		}
	}
	return nil
}
