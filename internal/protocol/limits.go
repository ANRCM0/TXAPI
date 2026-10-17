package protocol

import (
	"encoding/json"
	"errors"

	"minigateway/internal/protocol/adaptor"
)

// CheckOutputLimit checks the prepared wire request, not the canonical DTO.
// A completion limit may include reasoning and is not interchangeable with a
// legacy text limit. Unsupported conversions must reject rather than default.
func CheckOutputLimit(a adaptor.Adaptor, body []byte, limit int, field string) error {
	if limit <= 0 || (field != "max_tokens" && field != "max_completion_tokens") {
		return errors.New("missing explicit output limit")
	}
	pa, ok := a.(adaptor.ProtocolAdaptor)
	if !ok {
		return errors.New("upstream protocol has no verified output limit mapping")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return errors.New("invalid prepared upstream request")
	}
	actual := 0
	switch pa.Name() {
	case "openai":
		other := "max_tokens"
		if field == other {
			other = "max_completion_tokens"
		}
		if _, exists := fields[other]; exists {
			return errors.New("prepared request changed output limit semantics")
		}
		if json.Unmarshal(fields[field], &actual) != nil {
			return errors.New("prepared request lost the explicit output limit")
		}
		if !singleCandidate(fields["n"]) {
			return errors.New("prepared request changed the candidate count")
		}
	case "anthropic":
		if field != "max_tokens" {
			return errors.New("completion token limits have no verified cross-protocol mapping")
		}
		if json.Unmarshal(fields["max_tokens"], &actual) != nil {
			return errors.New("prepared request lost the explicit output limit")
		}
	case "gemini":
		if field != "max_tokens" {
			return errors.New("completion token limits have no verified cross-protocol mapping")
		}
		var generation map[string]json.RawMessage
		if json.Unmarshal(fields["generationConfig"], &generation) != nil || generation == nil ||
			json.Unmarshal(generation["maxOutputTokens"], &actual) != nil {
			return errors.New("prepared request lost the explicit output limit")
		}
		if !singleCandidate(generation["candidateCount"]) {
			return errors.New("prepared request changed the candidate count")
		}
	default:
		return errors.New("upstream protocol has no verified output limit mapping")
	}
	if actual != limit {
		return errors.New("prepared request did not preserve the explicit output limit")
	}
	return nil
}

func singleCandidate(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var count int
	return json.Unmarshal(raw, &count) == nil && count == 1
}
