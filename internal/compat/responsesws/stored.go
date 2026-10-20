package responsesws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	"minigateway/internal/protocol/responses"
)

type StoredCodec struct{}

func validateStoredItems(raw json.RawMessage) error {
	if _, ok := text(raw); ok {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > history.MaxContextItems {
		return errors.New("stored input requires bounded self-contained items")
	}
	for _, raw := range items {
		var item struct {
			Type      string
			Namespace json.RawMessage
		}
		if json.Unmarshal(raw, &item) != nil {
			return errors.New("invalid stored input item")
		}
		switch item.Type {
		case "", "message", "function_call", "function_call_output":
		default:
			return errors.New("stored WS input supports self-contained text and functions")
		}
		if (item.Type == "function_call" || item.Type == "function_call_output") && item.Namespace != nil {
			if _, valid := text(item.Namespace); !valid {
				return errors.New("invalid stored function namespace")
			}
		}
	}
	return nil
}

func storedRequest(raw json.RawMessage) (Request, error) {
	if len(raw) > history.MaxContextBytes {
		return Request{}, errors.New("stored request exceeds context limit")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return Request{}, errors.New("invalid stored request")
	}
	// This internal path is only selected for an explicitly stored local round.
	// The compiled/upstream representation deliberately has store=false.
	fields["store"] = json.RawMessage("true")
	data, err := json.Marshal(fields)
	if err != nil {
		return Request{}, err
	}
	return ParseStored(v2.Frame{Type: 1, Data: data})
}

func (StoredCodec) Compile(raw json.RawMessage, inherited []json.RawMessage, id string) (history.CompiledCreate, error) {
	var out history.CompiledCreate
	parsed, err := storedRequest(raw)
	if err != nil || !strings.HasPrefix(id, "resp_") || len(id) > 80 {
		return out, errors.New("invalid stored create")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(parsed.Initial.Data, &fields)
	var items []json.RawMessage
	if value, ok := text(fields["input"]); ok {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": value}}})
		items = []json.RawMessage{item}
	} else if json.Unmarshal(fields["input"], &items) != nil || items == nil {
		return out, errors.New("invalid stored input")
	}
	if len(items)+len(inherited) > history.MaxContextItems {
		return out, errors.New("stored context exceeds item limit")
	}
	for i, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return out, errors.New("invalid stored item")
		}
		if _, ok := item["type"]; !ok {
			item["type"] = json.RawMessage(`"message"`)
		}
		if _, present := item["id"]; !present {
			item["id"], _ = json.Marshal(fmt.Sprintf("item_tx_%s_%d", strings.TrimPrefix(id, "resp_"), i))
		}
		items[i], _ = json.Marshal(item)
	}
	full := make([]json.RawMessage, 0, len(inherited)+len(items))
	full = append(append(full, inherited...), items...)
	seen := map[string]bool{}
	wire := make([]json.RawMessage, len(full))
	for i, raw := range full {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return out, errors.New("invalid stored context")
		}
		key, valid := text(item["id"])
		if !valid || !storedItemID(key) || seen[key] {
			return out, errors.New("missing or duplicate stored item identity")
		}
		seen[key] = true
		typ, _ := text(item["type"])
		if typ == "message" || strings.HasPrefix(key, "item_tx_") {
			delete(item, "id")
		}
		wire[i], _ = json.Marshal(item)
	}
	fields["input"], _ = json.Marshal(wire)
	fields["store"] = json.RawMessage("false")
	delete(fields, "previous_response_id")
	out.JSON, err = json.Marshal(fields)
	if err != nil || len(out.JSON) > history.MaxContextBytes {
		return history.CompiledCreate{}, errors.New("stored context exceeds message limit")
	}
	// Revalidate the full input, including all inherited output items, rather
	// than trusting an old parent as proof of current codec support.
	compiled, err := storedRequest(out.JSON)
	if err != nil {
		return history.CompiledCreate{}, err
	}
	out.Input, out.PromptText = full, compiled.PromptText
	return out, nil
}

func storedItemID(id string) bool {
	if len(id) < 1 || len(id) > 256 {
		return false
	}
	for _, b := range []byte(id) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func (StoredCodec) Project(raw json.RawMessage, id, parent, model string) (history.StoredEvent, error) {
	var out history.StoredEvent
	if len(raw) > history.MaxContextBytes {
		return out, errors.New("stored event exceeds message limit")
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil || event == nil {
		return out, errors.New("invalid validated stored event")
	}
	typ, _ := text(event["type"])
	if value, present := event["response"]; present {
		var response map[string]json.RawMessage
		if json.Unmarshal(value, &response) != nil || response == nil {
			return out, errors.New("stored lifecycle event requires response")
		}
		response["id"], _ = json.Marshal(id)
		response["model"], _ = json.Marshal(model)
		response["store"] = json.RawMessage("true")
		response["previous_response_id"] = json.RawMessage("null")
		if parent != "" {
			response["previous_response_id"], _ = json.Marshal(parent)
		}
		delete(response, "conversation")
		event["response"], _ = json.Marshal(response)
		switch typ {
		case "response.completed", "response.incomplete", "response.failed", "response.cancelled":
			parsed, _, err := responses.ParseResponse(event["response"])
			if err != nil || json.Unmarshal(parsed.Output, &out.Output) != nil || out.Output == nil {
				return history.StoredEvent{}, errors.New("stored terminal document is unavailable")
			}
			out.Terminal, out.Response = true, bytes.Clone(event["response"])
		}
	}
	if _, present := event["response_id"]; present {
		event["response_id"], _ = json.Marshal(id)
	}
	out.JSON, _ = json.Marshal(event)
	if len(out.JSON) > history.MaxContextBytes {
		return history.StoredEvent{}, errors.New("projected stored event exceeds message limit")
	}
	return out, nil
}

// MapStoredModel validates the compiled self-contained representation, including
// historical function calls which the connection-local input subset excludes.
func MapStoredModel(frame v2.Frame, model string) (v2.Frame, error) {
	if frame.Type != 1 || model == "" || len(model) > 200 || strings.TrimSpace(model) != model || strings.ContainsAny(model, "*\r\n") {
		return v2.Frame{}, errors.New("invalid stored native target model")
	}
	request, err := storedRequest(frame.Data)
	if err != nil {
		return v2.Frame{}, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(request.Initial.Data, &fields)
	if _, parent := fields["previous_response_id"]; parent {
		return v2.Frame{}, errors.New("stored context cannot also use a native parent")
	}
	fields["model"], _ = json.Marshal(model)
	data, err := json.Marshal(fields)
	if err != nil || len(data) > history.MaxContextBytes {
		return v2.Frame{}, errors.New("mapped stored context exceeds message limit")
	}
	return v2.Frame{Type: 1, Data: data}, nil
}

var _ history.StoredCodec = StoredCodec{}
