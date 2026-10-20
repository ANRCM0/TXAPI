package responses

import (
	"encoding/json"
	"errors"
	"strings"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	"minigateway/internal/protocol/jsonutil"
)

// BackgroundHistoryCodec compiles authorized local history without retaining a
// native parent handle. Authorization, target binding and acceptance stay with
// the generation owner; this codec never reads or writes history.
type BackgroundHistoryCodec struct{}

func (BackgroundHistoryCodec) CompileBackground(raw json.RawMessage, inherited []json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > history.MaxContextBytes || len(inherited) > history.MaxContextItems {
		return nil, errors.New("background context exceeds limit")
	}
	request, err := ParseRequest(raw)
	if err != nil || !request.Background || present(request.Conversation) {
		return nil, errors.New("invalid background history request")
	}
	var input []json.RawMessage
	var text string
	if json.Unmarshal(request.Input, &text) == nil && present(request.Input) {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
		input = []json.RawMessage{item}
	} else if json.Unmarshal(request.Input, &input) != nil || input == nil {
		return nil, errors.New("background history input must be text or items")
	}
	if len(inherited)+len(input) > history.MaxContextItems {
		return nil, errors.New("background context exceeds item limit")
	}
	items := make([]json.RawMessage, 0, len(inherited)+len(input))
	seen := make(map[string]bool)
	size := 0
	for _, source := range [][]json.RawMessage{inherited, input} {
		for _, rawItem := range source {
			if len(rawItem) > history.MaxContextBytes-size || jsonutil.Validate(rawItem) != nil {
				return nil, errors.New("invalid or oversized background context item")
			}
			size += len(rawItem)
			var item map[string]json.RawMessage
			if json.Unmarshal(rawItem, &item) != nil || item == nil {
				return nil, errors.New("invalid background context item")
			}
			var typ string
			if value, ok := item["type"]; ok && json.Unmarshal(value, &typ) != nil {
				return nil, errors.New("invalid background context item type")
			}
			if value, ok := item["id"]; ok {
				var id string
				if json.Unmarshal(value, &id) != nil || id == "" || len(id) > 256 || seen[id] {
					return nil, errors.New("invalid or duplicate background context item identity")
				}
				for _, ch := range []byte(id) {
					if ch < 33 || ch > 126 {
						return nil, errors.New("invalid background context item identity")
					}
				}
				seen[id] = true
				if typ == "" || typ == "message" || strings.HasPrefix(id, "item_tx_") {
					delete(item, "id")
				}
			}
			compiled, err := json.Marshal(item)
			if err != nil {
				return nil, errors.New("invalid background context item")
			}
			items = append(items, compiled)
		}
	}
	fields := request.fields
	fields["input"], _ = json.Marshal(items)
	delete(fields, "previous_response_id")
	out, err := json.Marshal(fields)
	if err != nil || len(out) > history.MaxContextBytes {
		return nil, errors.New("background context exceeds request limit")
	}
	// Revalidate all inherited output with the current background capability
	// rules, instead of treating a stored parent's readiness as codec approval.
	if _, err = (BackgroundParser{}).ParseRequest(execution.Responses, out); err != nil {
		return nil, err
	}
	return out, nil
}

var _ history.BackgroundCompiler = BackgroundHistoryCodec{}
