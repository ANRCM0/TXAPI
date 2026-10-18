package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"

	jsonutil "minigateway/internal/foundation/contracts/json/v1"
)

type StrictRequest struct {
	OutputLimit      int
	OutputLimitField string
}

// The initial strict tariff only prices text token dimensions. Paid tools,
// media and unknown extensions must not silently enter token-only accounting.
func ValidateStrictRequest(body []byte, embeddings bool) error {
	_, err := ParseStrictRequest(body, embeddings)
	return err
}

// ParseStrictRequest retains the explicit limit for verification after protocol
// conversion. It does not estimate input tokens or prove a provider cost bound.
func ParseStrictRequest(body []byte, embeddings bool) (StrictRequest, error) {
	limits, err := parseStrictRequest(body, embeddings)
	if err != nil {
		return StrictRequest{}, err
	}
	return limits, nil
}

func parseStrictRequest(body []byte, embeddings bool) (StrictRequest, error) {
	var limits StrictRequest
	if !utf8.Valid(body) {
		return limits, errors.New("strict requests require valid UTF-8 JSON")
	}
	if err := uniqueJSON(body); err != nil {
		return limits, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return limits, errors.New("strict requests require a JSON object")
	}
	if name, ok := strictString(fields["model"]); !ok || name == "" {
		return limits, errors.New("strict requests require a model string")
	}
	if raw, ok := fields["user"]; ok {
		if _, valid := strictString(raw); !valid {
			return limits, errors.New("strict user must be a string")
		}
	}
	allowed := map[string]bool{"model": true, "user": true}
	if embeddings {
		for _, field := range []string{"input", "dimensions", "encoding_format"} {
			allowed[field] = true
		}
		if single, ok := strictString(fields["input"]); ok {
			if single == "" {
				return limits, errors.New("strict embeddings require nonempty text input")
			}
		} else {
			var batch []json.RawMessage
			if json.Unmarshal(fields["input"], &batch) != nil || len(batch) == 0 {
				return limits, errors.New("strict embeddings require a nonempty text batch")
			}
			for _, raw := range batch {
				if text, valid := strictString(raw); !valid || text == "" {
					return limits, errors.New("strict embeddings require nonempty text items")
				}
			}
		}
		if raw, ok := fields["dimensions"]; ok {
			var dimensions int
			if json.Unmarshal(raw, &dimensions) != nil || dimensions <= 0 {
				return limits, errors.New("strict dimensions must be a positive integer")
			}
		}
		if raw, ok := fields["encoding_format"]; ok {
			format, valid := strictString(raw)
			if !valid || (format != "float" && format != "base64") {
				return limits, errors.New("unsupported strict embedding encoding")
			}
		}
	} else {
		for _, field := range []string{"messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "presence_penalty", "frequency_penalty", "seed", "n", "service_tier"} {
			allowed[field] = true
		}
		if raw, exists := fields["service_tier"]; exists {
			if tier, valid := strictString(raw); !valid || tier != "default" {
				return limits, errors.New("strict text billing only supports an explicit default service tier")
			}
		}
		// Unknown per-message fields may carry tool/media charges.
		var rawMessages []map[string]json.RawMessage
		if json.Unmarshal(fields["messages"], &rawMessages) != nil || len(rawMessages) == 0 {
			return limits, errors.New("strict chat requires text-only messages")
		}
		for _, message := range rawMessages {
			if _, valid := strictString(message["content"]); !valid {
				return limits, errors.New("strict message content must be a string")
			}
			for key := range message {
				if key != "role" && key != "content" && key != "name" {
					return limits, errors.New("strict chat does not support tool or media message fields")
				}
			}
			if raw, ok := message["name"]; ok {
				if _, valid := strictString(raw); !valid {
					return limits, errors.New("strict message name must be a string")
				}
			}
			role, _ := strictString(message["role"])
			switch role {
			case "user", "assistant", "system", "developer":
			default:
				return limits, errors.New("unsupported strict message role")
			}
		}
		max, alternate := 0, 0
		if raw, ok := fields["max_tokens"]; ok {
			if json.Unmarshal(raw, &max) != nil || max <= 0 {
				return limits, errors.New("strict max_tokens must be positive")
			}
		}
		if raw, ok := fields["max_completion_tokens"]; ok {
			if json.Unmarshal(raw, &alternate) != nil || alternate <= 0 {
				return limits, errors.New("strict max_completion_tokens must be positive")
			}
		}
		if (max == 0) == (alternate == 0) {
			return limits, errors.New("strict chat requires exactly one explicit output token limit")
		}
		limits.OutputLimit, limits.OutputLimitField = max, "max_tokens"
		if alternate > 0 {
			limits.OutputLimit, limits.OutputLimitField = alternate, "max_completion_tokens"
		}
		if raw, ok := fields["n"]; ok {
			var n int
			if json.Unmarshal(raw, &n) != nil || n != 1 {
				return limits, errors.New("strict chat supports n=1 only")
			}
		}
		if raw, ok := fields["stream"]; ok {
			if !strictBool(raw) {
				return limits, errors.New("strict stream must be a boolean")
			}
		}
		if raw, ok := fields["stream_options"]; ok {
			var options map[string]json.RawMessage
			if json.Unmarshal(raw, &options) != nil || options == nil {
				return limits, errors.New("invalid strict stream options")
			}
			for key := range options {
				if key != "include_usage" || !strictBool(options[key]) {
					return limits, errors.New("unsupported strict stream option")
				}
			}
		}
	}
	for field := range fields {
		if !allowed[field] {
			return limits, errors.New("request includes an unpriced strict billing dimension or unsupported field")
		}
	}
	return limits, nil
}

func strictString(raw json.RawMessage) (string, bool) {
	var value string
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func strictBool(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false"))
}

// JSON decoders may disagree on repeated keys. Validate every object before a
// last-value-wins map can discard evidence, including escaped equivalent keys.
func uniqueJSON(body []byte) error { return jsonutil.Validate(body) }
