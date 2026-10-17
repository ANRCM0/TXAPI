// Package responses keeps the native Responses payload independent from Chat.
package responses

import (
	"encoding/json"
	"errors"
	"minigateway/internal/protocol/jsonutil"
	"unicode/utf8"
)

type Request struct {
	Model              string
	Stream             bool
	Background         bool
	Store              *bool
	PreviousResponseID string
	Conversation       json.RawMessage
	Input              json.RawMessage
	fields             map[string]json.RawMessage
}

// ParseRequest retains extensions for the native path. This is protocol
// validation, not owner authorization, capability approval, or cost admission.
func ParseRequest(body []byte) (Request, error) {
	var r Request
	if !utf8.Valid(body) {
		return r, errors.New("Responses JSON must be UTF-8")
	}
	if err := jsonutil.Validate(body); err != nil {
		return r, err
	}
	if err := json.Unmarshal(body, &r.fields); err != nil || r.fields == nil {
		return r, errors.New("Responses request must be an object")
	}
	if json.Unmarshal(r.fields["model"], &r.Model) != nil || r.Model == "" {
		return r, errors.New("Responses model is required")
	}
	for key, target := range map[string]*bool{"stream": &r.Stream, "background": &r.Background} {
		if raw, ok := r.fields[key]; ok {
			if string(raw) == "null" || json.Unmarshal(raw, target) != nil {
				return r, errors.New("invalid Responses boolean option")
			}
		}
	}
	if raw, ok := r.fields["store"]; ok {
		var store bool
		if string(raw) == "null" || json.Unmarshal(raw, &store) != nil {
			return r, errors.New("invalid Responses store option")
		}
		r.Store = &store
	}
	if raw, ok := r.fields["previous_response_id"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &r.PreviousResponseID) != nil || r.PreviousResponseID == "" {
			return r, errors.New("invalid previous_response_id")
		}
	}
	r.Conversation = append(json.RawMessage(nil), r.fields["conversation"]...)
	if r.PreviousResponseID != "" && len(r.Conversation) > 0 && string(r.Conversation) != "null" {
		return r, errors.New("conversation and previous_response_id are mutually exclusive")
	}
	r.Input = append(json.RawMessage(nil), r.fields["input"]...)
	return r, nil
}

// WithModel changes only the explicitly authorized routing model. In particular
// it does not turn a background request into a synchronous one or set store.
func (r Request) WithModel(model string) ([]byte, error) {
	if model == "" || r.fields == nil {
		return nil, errors.New("Responses target model is required")
	}
	fields := make(map[string]json.RawMessage, len(r.fields))
	for k, v := range r.fields {
		fields[k] = v
	}
	fields["model"], _ = json.Marshal(model)
	return json.Marshal(fields)
}
