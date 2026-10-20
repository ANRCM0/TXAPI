package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
)

func backgroundHistoryID(id string) bool {
	return strings.HasPrefix(id, "resp_") && len(id) > 5 && len(id) <= 80 && backgroundItemID(id)
}

func backgroundItemID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, ch := range []byte(id) {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

func backgroundPublicationItems(raw []json.RawMessage, id, scope string, output bool, seen map[string]bool) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(raw))
	for index, value := range raw {
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) != nil || fields == nil {
			return nil, errors.New("invalid background history item")
		}
		var typ string
		if value, ok := fields["type"]; ok && (string(value) == "null" || json.Unmarshal(value, &typ) != nil) {
			return nil, errors.New("invalid background history item type")
		}
		switch typ {
		case "", "message":
			if output {
				var role string
				var parts []map[string]json.RawMessage
				if json.Unmarshal(fields["role"], &role) != nil || role != "assistant" || json.Unmarshal(fields["content"], &parts) != nil || parts == nil {
					return nil, errors.New("background history output requires assistant text parts")
				}
				for _, part := range parts {
					var kind, text string
					if json.Unmarshal(part["type"], &kind) != nil || kind != "output_text" && kind != "refusal" {
						return nil, errors.New("unsupported background history output content")
					}
					field := "text"
					if kind == "refusal" {
						field = "refusal"
					}
					if string(part[field]) == "null" || json.Unmarshal(part[field], &text) != nil {
						return nil, errors.New("invalid background history output text")
					}
				}
			} else {
				var text, role string
				if present(fields["content"]) && json.Unmarshal(fields["content"], &text) == nil {
					_ = json.Unmarshal(fields["role"], &role)
					part := map[string]any{"type": "input_text", "text": text}
					if role == "assistant" {
						part["type"], part["annotations"] = "output_text", []any{}
					}
					fields["content"], _ = json.Marshal([]any{part})
				}
			}
		case "function_call":
		case "function_call_output":
			if output {
				return nil, errors.New("unsupported background history output item")
			}
		default:
			return nil, errors.New("background history supports only text and functions")
		}
		if present(fields["namespace"]) {
			return nil, errors.New("background history namespaces are unsupported")
		}
		if status, ok := fields["status"]; output && ok && string(status) != `"completed"` {
			return nil, errors.New("background history output item is not complete")
		}
		var itemID string
		if value, ok := fields["id"]; ok {
			if json.Unmarshal(value, &itemID) != nil || !backgroundItemID(itemID) {
				return nil, errors.New("invalid background history item identity")
			}
		} else {
			itemID = fmt.Sprintf("item_tx_bg_%s_%s_%d", strings.TrimPrefix(id, "resp_"), scope, index)
			fields["id"], _ = json.Marshal(itemID)
		}
		if seen[itemID] {
			return nil, errors.New("duplicate background history item identity")
		}
		seen[itemID] = true
		if typ == "" {
			fields["type"] = json.RawMessage(`"message"`)
		}
		item, _ := json.Marshal(fields)
		items = append(items, item)
	}
	return items, nil
}

func backgroundPublicationTools(fields map[string]json.RawMessage) error {
	if present(fields["tools"]) {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(fields["tools"], &tools) != nil {
			return errors.New("invalid background history tools")
		}
		for _, tool := range tools {
			if string(tool["type"]) != `"function"` || present(tool["namespace"]) {
				return errors.New("background history supports only top-level function tools")
			}
		}
	}
	var choice map[string]json.RawMessage
	if json.Unmarshal(fields["tool_choice"], &choice) == nil && choice != nil && (string(choice["type"]) != `"function"` || present(choice["namespace"])) {
		return errors.New("unsupported background history tool choice")
	}
	return nil
}

func (BackgroundHistoryCodec) PrepareBackgroundHistory(raw json.RawMessage, id string) (history.BackgroundHistoryInput, error) {
	var out history.BackgroundHistoryInput
	if !backgroundHistoryID(id) || len(raw) == 0 || len(raw) > history.MaxContextBytes {
		return out, errors.New("invalid or oversized background history request")
	}
	parsed, err := (BackgroundParser{}).ParseRequest(execution.Responses, raw)
	if err != nil {
		return out, err
	}
	r := parsed.(backgroundRequest).native
	if err := backgroundPublicationTools(r.fields); err != nil {
		return out, err
	}
	var input []json.RawMessage
	var text string
	if json.Unmarshal(r.Input, &text) == nil && present(r.Input) {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
		input = []json.RawMessage{item}
	} else if json.Unmarshal(r.Input, &input) != nil || input == nil {
		return out, errors.New("invalid background history input")
	}
	if len(input) > history.MaxContextItems {
		return out, errors.New("background history input exceeds item limit")
	}
	input, err = backgroundPublicationItems(input, id, "in", false, map[string]bool{})
	if err != nil {
		return out, err
	}
	r.fields["input"], _ = json.Marshal(input)
	out.Request, _ = json.Marshal(r.fields)
	if len(out.Request) > history.MaxContextBytes {
		return history.BackgroundHistoryInput{}, errors.New("background history input exceeds request limit")
	}
	if _, err = (BackgroundParser{}).ParseRequest(execution.Responses, out.Request); err != nil {
		return history.BackgroundHistoryInput{}, err
	}
	out.Input = input
	return out, nil
}

func (c BackgroundHistoryCodec) ProjectBackgroundHistory(request, response json.RawMessage, id, parent, model string) (history.BackgroundHistoryResult, error) {
	var out history.BackgroundHistoryResult
	if model == "" || len(model) > 200 || parent != "" && (!backgroundHistoryID(parent) || parent == id) || len(response) == 0 || len(response) > history.MaxContextBytes {
		return out, errors.New("invalid background history result identity or size")
	}
	prepared, err := c.PrepareBackgroundHistory(request, id)
	if err != nil {
		return out, err
	}
	original, err := ParseRequest(prepared.Request)
	if err != nil || original.Model != model {
		return out, errors.New("background history request model changed")
	}
	parsed, _, err := ParseResponse(response)
	if err != nil || parsed.Status != "completed" {
		return out, errors.New("background history result is not a known completion")
	}
	var output []json.RawMessage
	if json.Unmarshal(parsed.Output, &output) != nil || output == nil || len(prepared.Input)+len(output) > history.MaxContextItems {
		return out, errors.New("background history output is missing or exceeds item limit")
	}
	seen := make(map[string]bool)
	for _, raw := range prepared.Input {
		var item struct{ ID string }
		_ = json.Unmarshal(raw, &item)
		seen[item.ID] = true
	}
	output, err = backgroundPublicationItems(output, id, "out", true, seen)
	if err != nil {
		return out, err
	}
	items := append(append(make([]json.RawMessage, 0, len(prepared.Input)+len(output)), prepared.Input...), output...)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(prepared.Request, &fields)
	fields["input"], _ = json.Marshal(items)
	full, _ := json.Marshal(fields)
	if len(full) > history.MaxContextBytes {
		return out, errors.New("background history complete context exceeds request limit")
	}
	// Validate output as future input too: the response decoder alone does not
	// prove that function arguments or message content can be replayed.
	if _, err = (BackgroundParser{}).ParseRequest(execution.Responses, full); err != nil {
		return out, err
	}
	var document map[string]json.RawMessage
	_ = json.Unmarshal(response, &document)
	document["id"], _ = json.Marshal(id)
	document["model"], _ = json.Marshal(model)
	document["store"] = json.RawMessage("true")
	document["previous_response_id"] = json.RawMessage("null")
	if parent != "" {
		document["previous_response_id"], _ = json.Marshal(parent)
	}
	delete(document, "conversation")
	document["output"], _ = json.Marshal(output)
	projected, _ := json.Marshal(document)
	if len(projected) > history.MaxContextBytes {
		return out, errors.New("background history result exceeds response limit")
	}
	return history.BackgroundHistoryResult{Request: prepared.Request, Input: prepared.Input, Items: items, Response: projected}, nil
}

var _ history.BackgroundHistoryProjector = BackgroundHistoryCodec{}
