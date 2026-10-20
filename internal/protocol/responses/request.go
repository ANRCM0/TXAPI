// Package responses keeps the native Responses payload independent from Chat.
package responses

import (
	"encoding/json"
	"errors"
	"minigateway/internal/protocol/jsonutil"
	"strings"
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

func (r Request) StoreEnabled() bool { return r.Store == nil || *r.Store }

func (r Request) MaxOutputTokens() int {
	var n int
	_ = json.Unmarshal(r.fields["max_output_tokens"], &n)
	return n
}

// ValidateHTTP bounds the synchronous, stateless native surface. Unknown
// extension fields remain intact; recognized hosted capabilities fail closed.
func (r Request) ValidateHTTP() error {
	if r.Background || r.PreviousResponseID != "" || present(r.Conversation) {
		return errors.New("Responses background and stored conversation continuation are not supported")
	}
	if raw, ok := r.fields["max_output_tokens"]; ok {
		var n int
		if json.Unmarshal(raw, &n) != nil || n < 16 {
			return errors.New("max_output_tokens must be an integer of at least 16")
		}
	}
	if raw := r.fields["instructions"]; present(raw) {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return errors.New("Responses instructions must be text")
		}
	}
	for _, key := range []string{"audio", "modalities", "prompt"} {
		if present(r.fields[key]) {
			return errors.New("Responses hosted prompts and media options are not supported")
		}
	}
	if raw := r.fields["tools"]; present(raw) {
		if err := validateTools(raw); err != nil {
			return err
		}
	}
	if raw := r.fields["tool_choice"]; present(raw) {
		var choice string
		if json.Unmarshal(raw, &choice) == nil {
			if choice != "auto" && choice != "none" && choice != "required" {
				return errors.New("unsupported Responses tool choice")
			}
		} else {
			var choice struct {
				Type      string          `json:"type"`
				Name      string          `json:"name"`
				Namespace json.RawMessage `json:"namespace"`
			}
			if json.Unmarshal(raw, &choice) != nil || choice.Type != "function" || choice.Name == "" {
				return errors.New("unsupported Responses tool choice")
			}
			if err := validateNamespace(choice.Namespace); err != nil {
				return err
			}
		}
	}
	if !present(r.Input) {
		return errors.New("Responses input is required")
	}
	var input string
	if json.Unmarshal(r.Input, &input) == nil {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(r.Input, &items) != nil {
		return errors.New("Responses input must be text or an item array")
	}
	for _, raw := range items {
		var item struct {
			Type             string          `json:"type"`
			Role             string          `json:"role"`
			CallID           string          `json:"call_id"`
			Name             string          `json:"name"`
			Namespace        json.RawMessage `json:"namespace"`
			Arguments        *string         `json:"arguments"`
			Output           json.RawMessage `json:"output"`
			Content          json.RawMessage `json:"content"`
			EncryptedContent *string         `json:"encrypted_content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return errors.New("invalid Responses input item")
		}
		switch item.Type {
		case "", "message":
			if item.Role != "user" && item.Role != "assistant" && item.Role != "system" && item.Role != "developer" {
				return errors.New("invalid Responses message role")
			}
			if err := validateTextContent(item.Content); err != nil {
				return err
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" || item.Arguments == nil {
				return errors.New("invalid Responses function call")
			}
			if err := validateNamespace(item.Namespace); err != nil {
				return err
			}
		case "function_call_output":
			if err := validateNamespace(item.Namespace); err != nil {
				return err
			}
			if item.CallID == "" {
				return errors.New("Responses function output requires call_id")
			}
			if err := validateTextContent(item.Output); err != nil {
				return err
			}
		case "reasoning":
			// A bare provider item ID is not owner-authorized local history.
			// Require self-contained opaque bytes instead of a native lookup.
			if item.EncryptedContent == nil || *item.EncryptedContent == "" {
				return errors.New("Responses reasoning history requires encrypted_content")
			}
		default:
			return errors.New("unsupported Responses input item type")
		}
	}
	return nil
}

func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func validateTextContent(raw json.RawMessage) error {
	var text string
	if present(raw) && json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var parts []struct {
		Type    string  `json:"type"`
		Text    *string `json:"text"`
		Refusal *string `json:"refusal"`
	}
	if !present(raw) || json.Unmarshal(raw, &parts) != nil {
		return errors.New("Responses content must be text or text parts")
	}
	for _, part := range parts {
		if (part.Type == "input_text" || part.Type == "output_text") && part.Text != nil {
			continue
		}
		if part.Type == "refusal" && part.Refusal != nil {
			continue
		}
		return errors.New("Responses media content is not supported")
	}
	return nil
}

// PromptText is a legacy usage estimate projection, never a strict cost proof.
func (r Request) PromptText() string {
	var text strings.Builder
	var instructions string
	_ = json.Unmarshal(r.fields["instructions"], &instructions)
	text.WriteString(instructions)
	appendText := func(raw json.RawMessage) {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			text.WriteByte('\n')
			text.WriteString(value)
			return
		}
		var parts []struct {
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		}
		_ = json.Unmarshal(raw, &parts)
		for _, part := range parts {
			text.WriteByte('\n')
			text.WriteString(part.Text)
			text.WriteString(part.Refusal)
		}
	}
	var input string
	if json.Unmarshal(r.Input, &input) == nil {
		appendText(r.Input)
		if present(r.fields["tools"]) {
			text.WriteByte('\n')
			text.Write(r.fields["tools"])
		}
		return text.String()
	}
	var items []struct {
		Content   json.RawMessage `json:"content"`
		Output    json.RawMessage `json:"output"`
		Arguments string          `json:"arguments"`
		Name      string          `json:"name"`
	}
	_ = json.Unmarshal(r.Input, &items)
	for _, item := range items {
		appendText(item.Content)
		appendText(item.Output)
		text.WriteString(item.Name)
		text.WriteString(item.Arguments)
	}
	if present(r.fields["tools"]) {
		text.WriteByte('\n')
		text.Write(r.fields["tools"])
	}
	return text.String()
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
