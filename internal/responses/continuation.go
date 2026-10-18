package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	native "minigateway/internal/protocol/responses"
)

const maxHistoryItems = 1024
const maxHistoryContextBytes = 256 << 10

func stateFields(body []byte) (map[string]json.RawMessage, string, string, error) {
	var f map[string]json.RawMessage
	if _, err := canonical(body); err != nil {
		return nil, "", "", err
	}
	if json.Unmarshal(body, &f) != nil || f == nil {
		return nil, "", "", errors.New("request must be an object")
	}
	var parent, conv string
	if raw := f["previous_response_id"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &parent) != nil || parent == "" {
			return nil, "", "", errors.New("invalid previous_response_id")
		}
	}
	if raw := f["conversation"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &conv) != nil {
			var ref struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &ref) != nil {
				return nil, "", "", errors.New("invalid conversation")
			}
			conv = ref.ID
		}
		if conv == "" {
			return nil, "", "", errors.New("invalid conversation")
		}
	}
	if parent != "" && conv != "" {
		return nil, "", "", errors.New("conversation and previous_response_id are mutually exclusive")
	}
	if parent != "" && (!strings.HasPrefix(parent, "resp_") || len(parent) > 80) || conv != "" && (!strings.HasPrefix(conv, "conv_") || len(conv) > 80) {
		return nil, "", "", errors.New("invalid history identity")
	}
	delete(f, "previous_response_id")
	delete(f, "conversation")
	return f, parent, conv, nil
}

func normalizeItems(raw json.RawMessage) ([]json.RawMessage, error) {
	var text string
	var items []json.RawMessage
	if json.Unmarshal(raw, &text) == nil {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}})
		items = []json.RawMessage{item}
	} else if json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, errors.New("input must be text or items")
	}
	if len(items) > maxHistoryItems {
		return nil, state.ErrHistoryCapacity
	}
	for i, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return nil, errors.New("invalid item")
		}
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		if typ == "" {
			typ = "message"
			item["type"] = json.RawMessage(`"message"`)
		}
		if typ == "item_reference" {
			return nil, errors.New("item references are unsupported")
		}
		var id string
		_ = json.Unmarshal(item["id"], &id)
		if id == "" {
			random, err := randomID()
			if err != nil {
				return nil, err
			}
			id = "item_tx_" + random
			item["id"], _ = json.Marshal(id)
		}
		if !validKey(id) || len(id) > 256 {
			return nil, errors.New("invalid item identity")
		}
		items[i], _ = json.Marshal(item)
	}
	// Reuse native validation for the supported text/function/reasoning subset.
	input, _ := json.Marshal(items)
	check, _ := json.Marshal(map[string]any{"model": "validation", "input": json.RawMessage(input)})
	r, err := native.ParseRequest(check)
	if err == nil {
		err = r.ValidateHTTP()
	}
	return items, err
}

type historyTurn struct {
	record              state.ResponseHistory
	content             historyContent
	conversation        state.ResponseHistory
	conversationContent historyContent
	target              *execv1.TargetConstraint
}

func (h *History) prepare(ctx context.Context, b identity.Binding, receipt state.ResponseRequest, body []byte) ([]byte, *historyTurn, error) {
	fields, parent, conv, err := stateFields(body)
	if err != nil {
		return nil, nil, err
	}
	if !receipt.Store {
		if parent != "" || conv != "" {
			return nil, nil, errHistoryRequest
		}
		return body, nil, nil
	}
	input, err := normalizeItems(fields["input"])
	if err != nil {
		return nil, nil, errHistoryRequest
	}
	t := &historyTurn{record: h.fresh(receipt.ResponseID, "response", receipt.Model, b), content: historyContent{Parent: parent, Conversation: conv}}
	var guards []state.HistoryGuard
	if parent != "" || conv != "" {
		id := parent
		if conv != "" {
			id = conv
		}
		r, c, err := h.read(ctx, b, id)
		if err != nil {
			return nil, nil, err
		}
		if !c.Ready || r.Busy != "" || c.Active != "" || r.Model != "" && r.Model != receipt.Model {
			return nil, nil, state.ErrHistoryConflict
		}
		if parent != "" && r.Kind != "response" || conv != "" && r.Kind != "conversation" {
			return nil, nil, state.ErrHistoryNotFound
		}
		input = append(append([]json.RawMessage(nil), c.Items...), input...)
		if c.Target.Fingerprint != "" {
			copy := c.Target
			t.target = &copy
		}
		if parent != "" {
			guards = append(guards, state.HistoryGuard{ID: r.ID, Version: r.Version})
		} else {
			t.conversation, t.conversationContent = r, c
		}
	}
	if len(input) > maxHistoryItems {
		return nil, nil, state.ErrHistoryCapacity
	}
	seen := map[string]bool{}
	for _, raw := range input {
		var item struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &item)
		if seen[item.ID] {
			return nil, nil, errHistoryRequest
		}
		seen[item.ID] = true
	}
	t.content.Input = input
	t.content.Active = receipt.ID
	t.record.Busy = receipt.ID
	fields["input"], _ = json.Marshal(wireItems(input))
	// This application uses full local context exclusively. Native parent IDs
	// are retained as private provenance and are never simultaneously sent.
	effective, err := json.Marshal(fields)
	if err != nil || len(effective) > maxHistoryContextBytes {
		return nil, nil, state.ErrHistoryCapacity
	}
	t.content.Request = effective
	parsed, err := native.ParseRequest(effective)
	if err != nil {
		return nil, nil, err
	}
	if err = parsed.ValidateHTTP(); err != nil {
		return nil, nil, state.ErrHistoryContext
	}
	t.record, err = h.seal(t.record, t.content)
	if err != nil {
		return nil, nil, err
	}
	changes := []state.HistoryChange{{Record: t.record}}
	if conv != "" {
		old := t.conversation.Version
		t.conversation.Version++
		t.conversation.Model = receipt.Model
		t.conversation.Busy = receipt.ID
		t.conversationContent.Active = receipt.ID
		t.conversation, err = h.seal(t.conversation, t.conversationContent)
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, state.HistoryChange{Expected: old, Record: t.conversation})
	}
	if err = h.apply(ctx, b, changes, guards...); err != nil {
		return nil, nil, err
	}
	return effective, t, nil
}

func wireItems(items []json.RawMessage) []json.RawMessage {
	copy := make([]json.RawMessage, len(items))
	for i, raw := range items {
		var item map[string]json.RawMessage
		_ = json.Unmarshal(raw, &item)
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		var id string
		_ = json.Unmarshal(item["id"], &id)
		if typ == "message" || strings.HasPrefix(id, "item_tx_") {
			delete(item, "id")
		}
		copy[i], _ = json.Marshal(item)
	}
	return copy
}

// captureOutput retains a bounded copy of already validated encoded output.
// It never changes commitment, errors or downstream backpressure.
type captureOutput struct {
	execv1.Output
	body                 bytes.Buffer
	overflow             bool
	metadata             execv1.ResponseMetadata
	parent, conversation string
}

func (o *captureOutput) Commit(ctx context.Context, m execv1.ResponseMetadata) error {
	o.metadata = m
	return o.Output.Commit(ctx, m)
}
func (o *captureOutput) Write(ctx context.Context, p []byte) (int, error) {
	original := len(p)
	if o.parent != "" || o.conversation != "" {
		p = o.rewrite(p)
	}
	n, err := o.Output.Write(ctx, p)
	if n > 0 && n <= len(p) {
		if o.body.Len()+n > state.ResponseHistoryMaxBytes {
			o.overflow = true
		} else if !o.overflow {
			_, _ = o.body.Write(p[:n])
		}
	}
	if err == nil && n == len(p) {
		return original, nil
	}
	if n > 0 {
		return 0, errors.New("history output delivery incomplete")
	}
	return n, err
}
func (o *captureOutput) rewrite(p []byte) []byte {
	patch := func(raw []byte) []byte {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || obj == nil {
			return raw
		}
		var kind string
		_ = json.Unmarshal(obj["object"], &kind)
		if kind == "response" {
			if o.parent != "" {
				obj["previous_response_id"], _ = json.Marshal(o.parent)
			}
			if o.conversation != "" {
				obj["conversation"], _ = json.Marshal(map[string]string{"id": o.conversation})
			}
		} else if response := obj["response"]; len(response) > 0 {
			var inner map[string]json.RawMessage
			if json.Unmarshal(response, &inner) == nil {
				if o.parent != "" {
					inner["previous_response_id"], _ = json.Marshal(o.parent)
				}
				if o.conversation != "" {
					inner["conversation"], _ = json.Marshal(map[string]string{"id": o.conversation})
				}
				obj["response"], _ = json.Marshal(inner)
			}
		} else {
			return raw
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return raw
		}
		return out
	}
	if strings.Contains(o.metadata.ContentType, "event-stream") {
		lines := bytes.Split(p, []byte("\n"))
		for i, line := range lines {
			if bytes.HasPrefix(line, []byte("data:")) {
				lines[i] = append([]byte("data: "), patch(bytes.TrimSpace(line[5:]))...)
			}
		}
		return bytes.Join(lines, []byte("\n"))
	}
	return patch(p)
}
func (o *captureOutput) SetWriteDeadline(t time.Time) error {
	if d, ok := o.Output.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return d.SetWriteDeadline(t)
	}
	return http.ErrNotSupported
}

func terminalObject(o *captureOutput, stream bool) (json.RawMessage, error) {
	if o.overflow {
		return nil, state.ErrHistoryCapacity
	}
	if !stream {
		r, _, err := native.ParseResponse(o.body.Bytes())
		if err != nil || r.Output == nil {
			return nil, errors.New("complete native response unavailable")
		}
		return bytes.Clone(o.body.Bytes()), nil
	}
	var terminal json.RawMessage
	for _, block := range bytes.Split(o.body.Bytes(), []byte("\n\n")) {
		var data []string
		for _, line := range bytes.Split(block, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				data = append(data, strings.TrimSpace(string(line[5:])))
			}
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) == nil && (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed" || event.Type == "response.cancelled") {
			terminal = event.Response
		}
	}
	r, _, err := native.ParseResponse(terminal)
	if err != nil || r.Output == nil {
		return nil, errors.New("complete native response unavailable")
	}
	return terminal, nil
}

func (h *History) finish(b identity.Binding, t *historyTurn, result execv1.Outcome, capture *captureOutput, stream bool, known bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content := t.content
	content.Active = ""
	t.record.Busy = "uncertain"
	var resultErr error
	if known {
		content.Response, resultErr = terminalObject(capture, stream)
		if resultErr == nil {
			var response map[string]json.RawMessage
			_ = json.Unmarshal(content.Response, &response)
			var output []json.RawMessage
			resultErr = json.Unmarshal(response["output"], &output)
			if resultErr == nil && len(content.Input)+len(output) <= maxHistoryItems {
				content.Items = append(append([]json.RawMessage(nil), content.Input...), output...)
				content.Ready = true
				t.record.Busy = ""
				content.Target = result.Target
				content.PriceVersion = capture.metadata.Values["X-TXAPI-Price-Version"]
				content.BillingID = capture.metadata.Values["X-TXAPI-Billing-ID"]
				response["previous_response_id"], _ = json.Marshal(content.Parent)
				if content.Parent == "" {
					response["previous_response_id"] = json.RawMessage("null")
				}
				if content.Conversation != "" {
					response["conversation"], _ = json.Marshal(map[string]string{"id": content.Conversation})
				}
				content.Response, _ = json.Marshal(response)
			} else {
				resultErr = state.ErrHistoryCapacity
			}
		}
	}
	if !known || resultErr != nil {
		content.Ready = false
		content.Items = nil
		content.Input = nil
		content.Response = nil
		content.Request = nil
	}
	notDispatched := result.Phase == execv1.NotDispatched
	for _, attempt := range result.Attempts {
		notDispatched = notDispatched && attempt.Phase == execv1.NotDispatched
	}
	if notDispatched {
		t.record.Busy = ""
	}
	previous := t.record.Version
	t.record.Version++
	sealed, err := h.seal(t.record, content)
	if errors.Is(err, state.ErrHistoryCapacity) {
		content.Ready = false
		content.Items = nil
		content.Input = nil
		content.Response = nil
		content.Request = nil
		t.record.Busy = "uncertain"
		resultErr = err
		sealed, err = h.seal(t.record, content)
	}
	if err != nil {
		return err
	}
	changes := []state.HistoryChange{{Expected: previous, Record: sealed}}
	if t.conversation.ID != "" {
		conv := t.conversation
		c := t.conversationContent
		old := conv.Version
		conv.Version++
		// The encrypted marker also guards against an altered plaintext busy
		// column reopening an unknown generation after a crash or I/O failure.
		c.Active = "uncertain"
		conv.Busy = "uncertain"
		if content.Ready {
			c.Items = content.Items
			c.Target = content.Target
			conv.Busy = ""
			c.Active = ""
		}
		if notDispatched {
			conv.Busy = ""
			c.Active = ""
		}
		conv, err = h.seal(conv, c)
		if err != nil {
			return err
		}
		changes = append(changes, state.HistoryChange{Expected: old, Record: conv})
	}
	if err = h.apply(ctx, b, changes); err != nil {
		return err
	}
	return resultErr
}
