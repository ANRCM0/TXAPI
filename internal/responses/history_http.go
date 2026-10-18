package responses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"minigateway/internal/access"
	"minigateway/internal/model"
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

func historyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrHistoryNotFound):
		respond(w, 404, "not_found", "history is unavailable")
	case errors.Is(err, access.ErrUnauthorized):
		respond(w, 401, "unauthorized", "identity is no longer authorized")
	case errors.Is(err, model.ErrHistoryConflict):
		respond(w, 409, "history_conflict", "history changed or a generation is active or uncertain")
	case errors.Is(err, model.ErrHistoryCapacity):
		respond(w, 429, "history_capacity", "history capacity or context limit exceeded")
	case errors.Is(err, model.ErrHistoryContext):
		respond(w, 409, "history_context_unavailable", "complete self-contained history cannot be reconstructed")
	case errors.Is(err, errHistoryRequest):
		respond(w, 400, "invalid_request_error", "invalid or unsupported history request")
	default:
		respond(w, 503, "history_unavailable", "encrypted history could not be verified")
	}
}

func historyJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func historyBody(r *http.Request, allowed ...string) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
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

func conversationObject(row model.ResponseHistory, c historyContent) any {
	return map[string]any{"id": row.ID, "object": "conversation", "created_at": row.CreatedAt / 1000, "metadata": c.Metadata}
}

func listHistoryItems(r *http.Request, items []json.RawMessage) (any, error) {
	q := r.URL.Query()
	for k, v := range q {
		if len(v) != 1 || k != "after" && k != "limit" && k != "order" {
			return nil, errHistoryRequest
		}
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return nil, errHistoryRequest
		}
		limit = n
	}
	order := q.Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "" && order != "asc" && order != "desc" {
		return nil, errHistoryRequest
	}
	copy := append([]json.RawMessage(nil), items...)
	if order == "desc" {
		for i, j := 0, len(copy)-1; i < j; i, j = i+1, j-1 {
			copy[i], copy[j] = copy[j], copy[i]
		}
	}
	itemID := func(raw json.RawMessage) string {
		var i struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &i)
		return i.ID
	}
	start := 0
	if after := q.Get("after"); after != "" {
		start = -1
		for i, raw := range copy {
			if itemID(raw) == after {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, errHistoryRequest
		}
	}
	end := start + limit
	if end > len(copy) {
		end = len(copy)
	}
	data := copy[start:end]
	if data == nil {
		data = []json.RawMessage{}
	}
	var first, last any
	if len(data) > 0 {
		first = itemID(data[0])
		last = itemID(data[len(data)-1])
	}
	return map[string]any{"object": "list", "data": data, "first_id": first, "last_id": last, "has_more": end < len(copy)}, nil
}

func (s *Service) historyHTTP(w http.ResponseWriter, r *http.Request, b access.Binding) {
	h := s.history
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowHistoryRead(b.UserID) {
		w.Header().Set("Retry-After", "60")
		respond(w, 429, "rate_limit_error", "history delivery rate exceeded")
		return
	}
	// Reads/mutations consume bounded application delivery capacity, never a
	// generation's RPM/TPM or budget. Unknown query switches are not ignored.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		historyError(w, model.ErrHistoryNotFound)
		return
	}
	if len(parts) == 2 && parts[1] == "conversations" && r.Method == http.MethodPost {
		if len(r.URL.RawQuery) > 0 {
			historyError(w, errHistoryRequest)
			return
		}
		fields, err := historyBody(r, "metadata", "items")
		if err != nil {
			historyError(w, err)
			return
		}
		metadata, err := historyMetadata(fields["metadata"])
		if err != nil {
			historyError(w, err)
			return
		}
		items := []json.RawMessage{}
		if raw := fields["items"]; len(raw) > 0 {
			items, err = normalizeItems(raw)
			if err != nil {
				historyError(w, errHistoryRequest)
				return
			}
		}
		if len(items) > 20 {
			historyError(w, errHistoryRequest)
			return
		}
		id, err := randomID()
		if err != nil {
			historyError(w, err)
			return
		}
		row := h.fresh("conv_"+id, "conversation", "", b)
		content := historyContent{Ready: true, Items: items, Metadata: metadata}
		row, err = h.seal(row, content)
		if err == nil {
			err = h.apply(r.Context(), b, []model.HistoryChange{{Record: row}})
		}
		if err != nil {
			historyError(w, err)
			return
		}
		w.Header().Set("ETag", `"1"`)
		historyJSON(w, conversationObject(row, content))
		return
	}
	if len(parts) < 3 {
		historyError(w, model.ErrHistoryNotFound)
		return
	}
	id := parts[2]
	row, content, err := h.read(r.Context(), b, id)
	if err != nil {
		historyError(w, err)
		return
	}
	if parts[1] == "responses" && row.Kind != "response" || parts[1] == "conversations" && row.Kind != "conversation" {
		historyError(w, model.ErrHistoryNotFound)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatUint(row.Version, 10)+`"`)
	if row.Kind == "response" {
		if !content.Ready || row.Busy != "" {
			historyError(w, model.ErrHistoryConflict)
			return
		}
		switch {
		case len(parts) == 3 && r.Method == http.MethodGet && r.URL.RawQuery == "":
			historyJSON(w, content.Response)
		case len(parts) == 4 && parts[3] == "input_items" && r.Method == http.MethodGet:
			page, err := listHistoryItems(r, content.Input)
			if err != nil {
				historyError(w, err)
			} else {
				historyJSON(w, page)
			}
		case len(parts) == 3 && r.Method == http.MethodDelete && r.URL.RawQuery == "":
			if err = h.delete(r.Context(), b, row, content, r); err != nil {
				historyError(w, err)
				return
			}
			historyJSON(w, map[string]any{"id": id, "object": "response.deleted", "deleted": true})
		default:
			historyError(w, errHistoryRequest)
		}
		return
	}
	if row.Kind != "conversation" || parts[1] != "conversations" {
		historyError(w, model.ErrHistoryNotFound)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodGet && r.URL.RawQuery == "" {
		historyJSON(w, conversationObject(row, content))
		return
	}
	if len(parts) == 4 && parts[3] == "items" && r.Method == http.MethodGet {
		page, err := listHistoryItems(r, content.Items)
		if err != nil {
			historyError(w, err)
		} else {
			historyJSON(w, page)
		}
		return
	}
	if len(parts) == 5 && parts[3] == "items" && r.Method == http.MethodGet && r.URL.RawQuery == "" {
		for _, item := range content.Items {
			var obj struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(item, &obj)
			if obj.ID == parts[4] {
				historyJSON(w, item)
				return
			}
		}
		historyError(w, model.ErrHistoryNotFound)
		return
	}
	if r.URL.RawQuery != "" {
		historyError(w, errHistoryRequest)
		return
	}
	if row.Busy != "" || content.Active != "" {
		if !(len(parts) == 3 && r.Method == http.MethodDelete && row.Busy == "uncertain") {
			historyError(w, model.ErrHistoryConflict)
			return
		}
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		if err = h.delete(r.Context(), b, row, content, r); err != nil {
			historyError(w, err)
			return
		}
		historyJSON(w, map[string]any{"id": id, "object": "conversation.deleted", "deleted": true})
		return
	}
	old := row.Version
	if !historyMatch(r, row) {
		historyError(w, model.ErrHistoryConflict)
		return
	}
	var result any
	switch {
	case len(parts) == 3 && r.Method == http.MethodPost:
		fields, e := historyBody(r, "metadata")
		err = e
		if err == nil {
			if _, ok := fields["metadata"]; !ok {
				err = errHistoryRequest
			} else {
				content.Metadata, err = historyMetadata(fields["metadata"])
			}
		}
		result = conversationObject(row, content)
	case len(parts) == 4 && parts[3] == "items" && r.Method == http.MethodPost:
		fields, e := historyBody(r, "items")
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
		result = map[string]any{"object": "list", "data": items, "has_more": false, "first_id": nil, "last_id": nil}
		if len(items) > 0 {
			var a, z map[string]any
			_ = json.Unmarshal(items[0], &a)
			_ = json.Unmarshal(items[len(items)-1], &z)
			result.(map[string]any)["first_id"] = a["id"]
			result.(map[string]any)["last_id"] = z["id"]
		}
	case len(parts) == 5 && parts[3] == "items" && r.Method == http.MethodDelete:
		found := false
		for i, raw := range content.Items {
			var obj struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &obj)
			if obj.ID == parts[4] {
				content.Items = append(content.Items[:i:i], content.Items[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			err = model.ErrHistoryNotFound
		}
		result = conversationObject(row, content)
	default:
		err = errHistoryRequest
	}
	if err != nil {
		historyError(w, err)
		return
	}
	row.Version++
	row, err = h.seal(row, content)
	if err == nil {
		err = h.apply(r.Context(), b, []model.HistoryChange{{Expected: old, Record: row}})
	}
	if err != nil {
		historyError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatUint(row.Version, 10)+`"`)
	historyJSON(w, result)
}

func historyMatch(r *http.Request, row model.ResponseHistory) bool {
	match := r.Header.Get("If-Match")
	return match == "" || match == `"`+strconv.FormatUint(row.Version, 10)+`"`
}
func (h *History) delete(ctx context.Context, b access.Binding, row model.ResponseHistory, c historyContent, r *http.Request) error {
	if !historyMatch(r, row) {
		return model.ErrHistoryConflict
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
	return h.apply(ctx, b, []model.HistoryChange{{Expected: old, Record: row}})
}
