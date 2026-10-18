package responses

import (
	"context"
	"encoding/json"
	"errors"
	application "minigateway/internal/foundation/contracts/responses/v1"

	"time"
	"unicode/utf8"

	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

var errHistoryRequest = errors.New("invalid history request")

type historyReadWindow struct {
	started time.Time
	used    int
}

func (s *Service) allowHistoryRead(owner uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.readWindows == nil {
		s.readWindows = make(map[uint]historyReadWindow)
	}
	w, found := s.readWindows[owner]
	if !found && len(s.readWindows) >= 4096 {
		for id, entry := range s.readWindows {
			if now.Sub(entry.started) >= time.Minute {
				delete(s.readWindows, id)
			}
		}
		if len(s.readWindows) >= 4096 {
			return false
		}
	}
	if now.Sub(w.started) >= time.Minute {
		w = historyReadWindow{started: now}
	}
	if w.used >= 120 {
		return false
	}
	w.used++
	s.readWindows[owner] = w
	return true
}

func historyProblem(delivery application.Delivery, err error) {
	switch {
	case errors.Is(err, state.ErrHistoryNotFound):
		problem(delivery, "not_found", "history is unavailable")
	case errors.Is(err, identity.ErrUnauthorized):
		problem(delivery, "unauthorized", "identity is no longer authorized")
	case errors.Is(err, state.ErrHistoryConflict):
		problem(delivery, "history_conflict", "history changed or a generation is active or uncertain")
	case errors.Is(err, state.ErrHistoryCapacity):
		problem(delivery, "history_capacity", "history capacity or context limit exceeded")
	case errors.Is(err, state.ErrHistoryContext):
		problem(delivery, "history_context_unavailable", "complete self-contained history cannot be reconstructed")
	case errors.Is(err, errHistoryRequest):
		problem(delivery, "invalid_request_error", "invalid or unsupported history request")
	default:
		problem(delivery, "history_unavailable", "encrypted history could not be verified")
	}
}
func historyBody(command application.Command, allowed ...string) (map[string]json.RawMessage, error) {
	raw, err := readPayload(command)
	if err != nil || len(raw) > MaxRequestBytes {
		return nil, errHistoryRequest
	}
	if _, err := canonical(raw); err != nil {
		return nil, errHistoryRequest
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errHistoryRequest
	}
	for field := range obj {
		found := false
		for _, a := range allowed {
			found = found || field == a
		}
		if !found {
			return nil, errHistoryRequest
		}
	}
	return obj, nil
}

func historyMetadata(raw json.RawMessage) (map[string]string, error) {
	m := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return m, nil
	}
	if json.Unmarshal(raw, &m) != nil || len(m) > 16 {
		return nil, errHistoryRequest
	}
	for k, v := range m {
		if utf8.RuneCountInString(k) > 64 || utf8.RuneCountInString(v) > 512 {
			return nil, errHistoryRequest
		}
	}
	return m, nil
}

func conversationDocument(row state.ResponseHistory, c historyContent) application.Document {
	return application.Document{Kind: application.ConversationDocument, Conversation: application.Conversation{ID: row.ID, CreatedAt: row.CreatedAt, Metadata: c.Metadata}}
}
func historyItemID(raw json.RawMessage) string {
	var item struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &item)
	return item.ID
}
func pageDocument(items []json.RawMessage, more bool) application.Document {
	var first, last *string
	if len(items) > 0 {
		a, z := historyItemID(items[0]), historyItemID(items[len(items)-1])
		first, last = &a, &z
	}
	return application.Document{Kind: application.ItemsDocument, Page: application.Page{Items: items, FirstID: first, LastID: last, HasMore: more}}
}
func listHistoryItems(query application.PageQuery, items []json.RawMessage) (application.Document, error) {
	if query.Invalid {
		return application.Document{}, errHistoryRequest
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	order := query.Order
	if order == "" {
		order = "desc"
	}
	if limit < 1 || limit > 100 || order != "asc" && order != "desc" {
		return application.Document{}, errHistoryRequest
	}
	copy := append([]json.RawMessage(nil), items...)
	if order == "desc" {
		for i, j := 0, len(copy)-1; i < j; i, j = i+1, j-1 {
			copy[i], copy[j] = copy[j], copy[i]
		}
	}
	start := 0
	if query.After != "" {
		start = -1
		for i, raw := range copy {
			if historyItemID(raw) == query.After {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return application.Document{}, errHistoryRequest
		}
	}
	end := min(start+limit, len(copy))
	data := copy[start:end]
	if data == nil {
		data = []json.RawMessage{}
	}
	return pageDocument(data, end < len(copy)), nil
}

func (s *Service) historyCommand(ctx context.Context, delivery application.Delivery, command application.Command, b identity.Binding) {
	h := s.history
	delivery.Annotate(application.Metadata{NoStore: true})
	if command.Resource != "response" && command.Resource != "conversation" {
		historyProblem(delivery, state.ErrHistoryNotFound)
		return
	}
	if command.Action == application.CreateConversation {
		if command.HasQuery {
			historyProblem(delivery, errHistoryRequest)
			return
		}
		fields, err := historyBody(command, "metadata", "items")
		if err != nil {
			historyProblem(delivery, err)
			return
		}
		metadata, err := historyMetadata(fields["metadata"])
		if err != nil {
			historyProblem(delivery, err)
			return
		}
		items := []json.RawMessage{}
		if raw := fields["items"]; len(raw) > 0 {
			items, err = normalizeItems(raw)
			if err != nil {
				historyProblem(delivery, errHistoryRequest)
				return
			}
		}
		if len(items) > 20 {
			historyProblem(delivery, errHistoryRequest)
			return
		}
		id, err := randomID()
		if err != nil {
			historyProblem(delivery, err)
			return
		}
		row := h.fresh("conv_"+id, "conversation", "", b)
		content := historyContent{Ready: true, Items: items, Metadata: metadata}
		row, err = h.seal(row, content)
		if err == nil {
			err = h.apply(ctx, b, []state.HistoryChange{{Record: row}})
		}
		if err != nil {
			historyProblem(delivery, err)
			return
		}
		delivery.Annotate(application.Metadata{Version: 1})
		delivery.Document(conversationDocument(row, content))
		return
	}
	id := command.ID
	row, content, err := h.read(ctx, b, id)
	if err != nil {
		historyProblem(delivery, err)
		return
	}
	if command.Resource == application.ResponseResource && row.Kind != "response" || command.Resource == application.ConversationResource && row.Kind != "conversation" {
		historyProblem(delivery, state.ErrHistoryNotFound)
		return
	}
	delivery.Annotate(application.Metadata{Version: row.Version})
	if row.Kind == "response" {
		if !content.Ready || row.Busy != "" {
			historyProblem(delivery, state.ErrHistoryConflict)
			return
		}
		switch {
		case command.Action == application.GetResponse && !command.HasQuery:
			delivery.Document(application.Document{Kind: application.NativeDocument, Raw: content.Response})
		case command.Action == application.ListInputItems:
			page, err := listHistoryItems(command.Page, content.Input)
			if err != nil {
				historyProblem(delivery, err)
			} else {
				delivery.Document(page)
			}
		case command.Action == application.DeleteResponse && !command.HasQuery:
			if err = h.delete(ctx, b, row, content, command); err != nil {
				historyProblem(delivery, err)
				return
			}
			delivery.Document(application.Document{Kind: application.DeletedResponseDocument, ID: id})
		default:
			historyProblem(delivery, errHistoryRequest)
		}
		return
	}
	if row.Kind != "conversation" || command.Resource != application.ConversationResource {
		historyProblem(delivery, state.ErrHistoryNotFound)
		return
	}
	if command.Action == application.GetConversation && !command.HasQuery {
		delivery.Document(conversationDocument(row, content))
		return
	}
	if command.Action == application.ListItems {
		page, err := listHistoryItems(command.Page, content.Items)
		if err != nil {
			historyProblem(delivery, err)
		} else {
			delivery.Document(page)
		}
		return
	}
	if command.Action == application.GetItem && !command.HasQuery {
		for _, item := range content.Items {
			var obj struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(item, &obj)
			if obj.ID == command.ItemID {
				delivery.Document(application.Document{Kind: application.NativeDocument, Raw: item})
				return
			}
		}
		historyProblem(delivery, state.ErrHistoryNotFound)
		return
	}
	if command.HasQuery {
		historyProblem(delivery, errHistoryRequest)
		return
	}
	if row.Busy != "" || content.Active != "" {
		if !(command.Action == application.DeleteConversation && row.Busy == "uncertain") {
			historyProblem(delivery, state.ErrHistoryConflict)
			return
		}
	}
	if command.Action == application.DeleteConversation {
		if err = h.delete(ctx, b, row, content, command); err != nil {
			historyProblem(delivery, err)
			return
		}
		delivery.Document(application.Document{Kind: application.DeletedConversationDocument, ID: id})
		return
	}
	old := row.Version
	if !historyMatch(command, row) {
		historyProblem(delivery, state.ErrHistoryConflict)
		return
	}
	var result application.Document
	switch {
	case command.Action == application.UpdateConversation:
		fields, e := historyBody(command, "metadata")
		err = e
		if err == nil {
			if _, ok := fields["metadata"]; !ok {
				err = errHistoryRequest
			} else {
				content.Metadata, err = historyMetadata(fields["metadata"])
			}
		}
		result = conversationDocument(row, content)
	case command.Action == application.AddItems:
		fields, e := historyBody(command, "items")
		err = e
		var items []json.RawMessage
		if err == nil {
			items, err = normalizeItems(fields["items"])
			if err != nil {
				err = errHistoryRequest
			}
		}
		if err == nil && (len(items) < 1 || len(items) > 20 || len(content.Items)+len(items) > maxHistoryItems) {
			err = errHistoryRequest
		}
		if err == nil {
			seen := map[string]bool{}
			for _, raw := range append(append([]json.RawMessage(nil), content.Items...), items...) {
				var obj struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(raw, &obj)
				if seen[obj.ID] {
					err = errHistoryRequest
				}
				seen[obj.ID] = true
			}
			content.Items = append(content.Items, items...)
		}
		result = pageDocument(items, false)
	case command.Action == application.DeleteItem:
		found := false
		for i, raw := range content.Items {
			var obj struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &obj)
			if obj.ID == command.ItemID {
				content.Items = append(content.Items[:i:i], content.Items[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			err = state.ErrHistoryNotFound
		}
		result = conversationDocument(row, content)
	default:
		err = errHistoryRequest
	}
	if err != nil {
		historyProblem(delivery, err)
		return
	}
	row.Version++
	row, err = h.seal(row, content)
	if err == nil {
		err = h.apply(ctx, b, []state.HistoryChange{{Expected: old, Record: row}})
	}
	if err != nil {
		historyProblem(delivery, err)
		return
	}
	delivery.Annotate(application.Metadata{Version: row.Version})
	delivery.Document(result)
}

func historyMatch(command application.Command, row state.ResponseHistory) bool {
	return !command.InvalidVersion && (command.ExpectedVersion == nil || *command.ExpectedVersion == row.Version)
}
func (h *History) delete(ctx context.Context, b identity.Binding, row state.ResponseHistory, c historyContent, command application.Command) error {
	if !historyMatch(command, row) {
		return state.ErrHistoryConflict
	}
	old := row.Version
	row.Version++
	row.Deleted = true
	row.Busy = ""
	if row.Kind == "response" {
		row.Ciphertext = []byte{}
		row.ReservedBytes = 0
	} else {
		var err error
		row, err = h.seal(row, c)
		if err != nil {
			return err
		}
	}
	return h.apply(ctx, b, []state.HistoryChange{{Expected: old, Record: row}})
}
