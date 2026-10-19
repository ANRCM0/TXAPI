// Package responsesws bounds the single-lane text/function native WS subset.
package responsesws

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
	"minigateway/internal/protocol/responses"
)

type Request struct {
	Model      string
	PreviousID string
	PromptText string
	Store      bool
	Initial    v2.Frame
}

func object(body []byte, allowed string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, errors.New("expected JSON object")
	}
	names := map[string]bool{}
	for _, key := range strings.Fields(allowed) {
		names[key] = true
	}
	for key, value := range fields {
		if !names[key] || bytes.Equal(value, []byte("null")) {
			return nil, errors.New("unsupported or null field")
		}
	}
	return fields, nil
}
func text(raw json.RawMessage) (string, bool) {
	var s string
	err := json.Unmarshal(raw, &s)
	return s, len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && err == nil
}

func Parse(frame v2.Frame) (Request, error) {
	return parse(frame, false)
}

// ParseStored adds explicit local storage only when selected by assembly.
// The upstream frame still uses store=false; durable parent authorization is L1.
func ParseStored(frame v2.Frame) (Request, error) {
	return parse(frame, true)
}

func parse(frame v2.Frame, persistent bool) (Request, error) {
	var out Request
	if frame.Type != 1 || len(frame.Data) > 256<<10 || jsonvalue.Validate(frame.Data) != nil {
		return out, errors.New("invalid native WS request")
	}
	fields, err := object(frame.Data, "type model input instructions previous_response_id tools tool_choice parallel_tool_calls max_output_tokens temperature top_p metadata generate stream_id store")
	if err != nil {
		return out, err
	}
	typ, _ := text(fields["type"])
	out.Model, _ = text(fields["model"])
	if typ != "response.create" || out.Model == "" || len(out.Model) > 200 {
		return out, errors.New("response.create and explicit model required")
	}
	if raw, ok := fields["previous_response_id"]; ok {
		var valid bool
		out.PreviousID, valid = text(raw)
		if !valid || out.PreviousID == "" || len(out.PreviousID) > 256 {
			return out, errors.New("invalid continuation ID")
		}
	}
	if persistent && string(fields["store"]) == "true" {
		out.Store = true
	}
	for key, expected := range map[string]string{"store": "false", "stream_id": "\"main\"", "generate": "true"} {
		if key == "store" && out.Store {
			continue
		}
		if raw, ok := fields[key]; ok && string(raw) != expected {
			return out, errors.New("unsupported native WS option")
		}
	}
	fields["store"] = json.RawMessage("false")
	fields["stream_id"] = json.RawMessage("\"main\"")
	for _, key := range []string{"parallel_tool_calls"} {
		if raw, ok := fields[key]; ok {
			var b bool
			if json.Unmarshal(raw, &b) != nil {
				return out, errors.New("invalid boolean option")
			}
		}
	}
	for key, maximum := range map[string]float64{"temperature": 2, "top_p": 1} {
		if raw, ok := fields[key]; ok {
			var n float64
			if json.Unmarshal(raw, &n) != nil || n < 0 || n > maximum {
				return out, errors.New("invalid sampling option")
			}
		}
	}
	if raw, ok := fields["metadata"]; ok {
		var m map[string]string
		if json.Unmarshal(raw, &m) != nil || len(m) > 16 {
			return out, errors.New("invalid metadata")
		}
		for k, v := range m {
			if len(k) > 64 || len(v) > 512 {
				return out, errors.New("metadata exceeds limit")
			}
		}
	}
	if !out.Store {
		if err := validateInput(fields["input"]); err != nil {
			return out, err
		}
	} else if err := validateStoredItems(fields["input"]); err != nil {
		return out, err
	}
	if raw, ok := fields["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil {
			return out, errors.New("invalid tools")
		}
		for _, raw := range tools {
			tool, err := object(raw, "type name description parameters strict")
			if err != nil {
				return out, err
			}
			typ, _ := text(tool["type"])
			if typ != "function" {
				return out, errors.New("only function tools supported")
			}
			if raw, ok := tool["description"]; ok {
				if _, valid := text(raw); !valid {
					return out, errors.New("function description must be text")
				}
			}
			if raw, ok := tool["strict"]; ok {
				var b bool
				if json.Unmarshal(raw, &b) != nil {
					return out, errors.New("invalid strict option")
				}
			}
			if raw, ok := tool["parameters"]; ok {
				var schema map[string]json.RawMessage
				if json.Unmarshal(raw, &schema) != nil || schema == nil {
					return out, errors.New("invalid function parameters")
				}
			}
		}
	}
	if raw, ok := fields["tool_choice"]; ok {
		if _, isText := text(raw); !isText {
			if _, err := object(raw, "type name"); err != nil {
				return out, err
			}
		}
	}
	// Reuse the existing pure HTTP subset validator after removing WS-only
	// fields. Continuation authorization stays with the host, not the codec.
	validation := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		validation[k] = v
	}
	for _, key := range []string{"type", "previous_response_id", "generate", "stream_id"} {
		delete(validation, key)
	}
	body, _ := json.Marshal(validation)
	parsed, err := responses.ParseRequest(body)
	if err != nil {
		return out, err
	}
	if err = parsed.ValidateHTTP(); err != nil {
		return out, err
	}
	out.PromptText = parsed.PromptText()
	body, err = json.Marshal(fields)
	out.Initial = v2.Frame{Type: 1, Data: body}
	return out, err
}

func validateInput(raw json.RawMessage) error {
	if _, ok := text(raw); ok {
		return nil
	}
	var items []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil || bytes.Equal(raw, []byte("null")) {
		return errors.New("input must be text or items")
	}
	for _, raw := range items {
		var probe struct{ Type string }
		if json.Unmarshal(raw, &probe) != nil {
			return errors.New("invalid input item")
		}
		allowed := "type role content"
		contentKey := "content"
		if probe.Type == "function_call_output" {
			allowed = "type call_id output"
			contentKey = "output"
		} else if probe.Type != "" && probe.Type != "message" {
			return errors.New("unsupported input item")
		}
		item, err := object(raw, allowed)
		if err != nil {
			return err
		}
		content := item[contentKey]
		if _, ok := text(content); ok {
			continue
		}
		var parts []json.RawMessage
		if len(content) == 0 || json.Unmarshal(content, &parts) != nil || bytes.Equal(content, []byte("null")) {
			return errors.New("invalid text content")
		}
		for _, part := range parts {
			p, err := object(part, "type text")
			if err != nil {
				return err
			}
			typ, _ := text(p["type"])
			if typ != "input_text" && typ != "output_text" {
				return errors.New("unsupported content part")
			}
			if _, ok := text(p["text"]); !ok {
				return errors.New("missing text")
			}
		}
	}
	return nil
}

type Decoder struct {
	model    string
	stream   v3.StreamDecoder
	terminal bool
	started  bool
}

// IsTerminal classifies an already delivered event without retaining decoder state.
func IsTerminal(frame v2.Frame) bool {
	if frame.Type != 1 || len(frame.Data) > 256<<10 || jsonvalue.Validate(frame.Data) != nil {
		return false
	}
	var event struct {
		Type     string
		Response json.RawMessage
		Error    map[string]json.RawMessage
	}
	if json.Unmarshal(frame.Data, &event) != nil {
		return false
	}
	model := ""
	switch event.Type {
	case "response.completed", "response.incomplete", "response.failed", "response.cancelled":
		var response struct{ Model string }
		if json.Unmarshal(event.Response, &response) != nil || response.Model == "" {
			return false
		}
		model = response.Model
	case "error":
		code, codeOK := text(event.Error["code"])
		message, messageOK := text(event.Error["message"])
		if !codeOK || code == "" || !messageOK || message == "" {
			return false
		}
	default:
		return false
	}
	decoder := NewDecoder(model)
	decoder.started = true
	envelope, err := decoder.Decode(frame)
	return err == nil && envelope.ProtocolTerminal
}

func NewDecoder(model string) *Decoder {
	return &Decoder{model: model, stream: responses.NewDecoder().NewUsageEnvelopeStream()}
}

func (d *Decoder) Decode(frame v2.Frame) (v2.Envelope, error) {
	out, err := d.DecodeUsage(frame)
	return out.Execution, err
}

// DecodeUsage preserves native usage dimensions through the existing Responses parser.
func (d *Decoder) DecodeUsage(frame v2.Frame) (v3.Envelope, error) {
	var empty v3.Envelope
	if d.terminal || frame.Type != 1 || len(frame.Data) > 256<<10 || jsonvalue.Validate(frame.Data) != nil {
		return empty, errors.New("invalid native WS event")
	}
	var event struct {
		Type       string
		StreamID   string `json:"stream_id"`
		Response   json.RawMessage
		ResponseID string `json:"response_id"`
	}
	if json.Unmarshal(frame.Data, &event) != nil || event.StreamID != "" && event.StreamID != "main" {
		return empty, errors.New("invalid native WS lane")
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(frame.Data, &root)
	if raw, ok := root["stream_id"]; ok {
		lane, valid := text(raw)
		if !valid || lane != "main" {
			return empty, errors.New("invalid native WS lane")
		}
	}
	if !d.started && event.Type != "response.created" && event.Type != "error" {
		return empty, errors.New("native WS response has not started")
	}
	if d.started && event.Type == "response.created" {
		return empty, errors.New("duplicate native WS response")
	}
	switch event.Type {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed", "response.cancelled", "response.output_text.delta", "response.output_text.done", "response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done", "response.function_call_arguments.delta", "response.function_call_arguments.done", "error":
	default:
		return empty, errors.New("unsupported native WS event")
	}
	switch event.Type {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed", "response.cancelled":
		if len(event.Response) == 0 || bytes.Equal(event.Response, []byte("null")) {
			return empty, errors.New("native WS lifecycle event requires a response")
		}
	case "response.output_text.delta", "response.function_call_arguments.delta":
		if _, valid := text(root["delta"]); !valid {
			return empty, errors.New("native WS delta must be text")
		}
	case "response.output_text.done":
		if _, valid := text(root["text"]); !valid {
			return empty, errors.New("native WS output must be text")
		}
	case "response.function_call_arguments.done":
		if _, valid := text(root["arguments"]); !valid {
			return empty, errors.New("native WS function arguments must be text")
		}
	case "response.output_item.added", "response.output_item.done":
		if _, present := root["item"]; !present {
			return empty, errors.New("native WS item event requires an item")
		}
	case "response.content_part.added", "response.content_part.done":
		if _, present := root["part"]; !present {
			return empty, errors.New("native WS content event requires a part")
		}
	}
	if len(event.Response) > 0 {
		var r struct {
			Model  string
			Status string
			Output []json.RawMessage
		}
		if json.Unmarshal(event.Response, &r) != nil || r.Model != d.model {
			return empty, errors.New("native WS model mismatch")
		}
		if (event.Type == "response.created" || event.Type == "response.in_progress") && r.Status != "in_progress" {
			return empty, errors.New("native WS lifecycle state mismatch")
		}
		for _, item := range r.Output {
			if err := validateOutputItem(item); err != nil {
				return empty, err
			}
		}
	}
	if raw, ok := root["item"]; ok {
		if err := validateOutputItem(raw); err != nil {
			return empty, err
		}
	}
	if raw, ok := root["part"]; ok {
		if err := validateOutputPart(raw); err != nil {
			return empty, err
		}
	}
	var envelopes []v3.Envelope
	// Decode one complete message through the existing native facts parser.
	// JSON messages may contain whitespace/newlines, so compact before framing.
	var compact bytes.Buffer
	_ = json.Compact(&compact, frame.Data)
	lines := [][]byte{append([]byte("data: "), compact.Bytes()...), {}}
	for _, line := range lines {
		out, err := d.stream.DecodeUsageEnvelopes(line)
		if err != nil {
			return empty, err
		}
		envelopes = append(envelopes, out...)
	}
	if len(envelopes) != 1 {
		return empty, errors.New("native WS event did not produce one envelope")
	}
	out := envelopes[0]
	d.terminal = out.Execution.ProtocolTerminal
	d.started = true
	// Strip only the SSE representation; non-error WS events keep exact bytes.
	if event.Type == "error" {
		out.Execution.Data = []byte("{\"type\":\"error\",\"error\":{\"code\":\"upstream_error\",\"message\":\"Upstream generation failed.\"}}")
	} else {
		out.Execution.Data = bytes.Clone(frame.Data)
		if raw := root["response"]; len(raw) > 0 {
			var response map[string]json.RawMessage
			_ = json.Unmarshal(raw, &response)
			if detail, ok := response["error"]; ok && string(detail) != "null" {
				response["error"] = json.RawMessage("{\"code\":\"upstream_error\",\"message\":\"Upstream generation failed.\"}")
				root["response"], _ = json.Marshal(response)
				out.Execution.Data, _ = json.Marshal(root)
			}
		}
	}
	return out, nil
}

func validateOutputItem(raw json.RawMessage) error {
	var item struct {
		Type    string
		Content []json.RawMessage
	}
	if json.Unmarshal(raw, &item) != nil {
		return errors.New("invalid native WS output item")
	}
	if item.Type == "function_call" {
		return nil
	}
	if item.Type != "message" {
		return errors.New("unsupported native WS output item")
	}
	for _, part := range item.Content {
		if err := validateOutputPart(part); err != nil {
			return err
		}
	}
	return nil
}
func validateOutputPart(raw json.RawMessage) error {
	var part struct{ Type string }
	if json.Unmarshal(raw, &part) != nil || part.Type != "output_text" && part.Type != "refusal" {
		return errors.New("unsupported native WS content part")
	}
	return nil
}

// MapModel preserves the bounded native request while applying the host's
// already-authorized target mapping. It performs no routing or network I/O.
func MapModel(frame v2.Frame, model string) (v2.Frame, error) {
	if model == "" || len(model) > 200 || strings.TrimSpace(model) != model || strings.ContainsAny(model, "*\r\n") {
		return v2.Frame{}, errors.New("invalid native target model")
	}
	request, err := Parse(frame)
	if err != nil {
		return v2.Frame{}, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(request.Initial.Data, &fields); err != nil {
		return v2.Frame{}, err
	}
	fields["model"], err = json.Marshal(model)
	if err != nil {
		return v2.Frame{}, err
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return v2.Frame{}, err
	}
	if len(body) > 256<<10 {
		return v2.Frame{}, errors.New("mapped native request exceeds message limit")
	}
	return v2.Frame{Type: 1, Data: body}, nil
}
