package responseshttp

import (
	"encoding/json"
	"io"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	app "minigateway/internal/foundation/contracts/responses/v1"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type handler struct {
	service app.Service
	output  func(http.ResponseWriter) execution.DeadlineOutput
}

func missing(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}
func New(service app.Service, output func(http.ResponseWriter) execution.DeadlineOutput) http.Handler {
	if missing(service) || output == nil {
		panic("Responses application and output factory must be supplied")
	}
	return &handler{service: service, output: output}
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	command := parse(r)
	out := &delivery{DeadlineOutput: h.output(w), writer: w}
	call, problem := h.service.Begin(r.Context(), command.Action)
	if problem != nil {
		out.Problem(*problem)
		return
	}
	defer call.Close()
	command.Payload = &bodySource{reader: r.Body}
	call.Handle(command, out)
}

type bodySource struct{ reader io.Reader }

func (s *bodySource) Read() ([]byte, error) {
	if s.reader == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(s.reader, app.MaxRequestBytes+1))
}
func parse(r *http.Request) app.Command {
	c := app.Command{HasQuery: r.URL.RawQuery != "", IdempotencyKeys: append([]string(nil), r.Header.Values("Idempotency-Key")...)}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) < 2 || p[0] != "v1" {
		return c
	}
	if len(p) > 2 {
		c.ID = p[2]
	}
	if p[1] == "responses" {
		c.Resource = app.ResponseResource
		switch {
		case len(p) == 2 && r.Method == http.MethodPost:
			c.Action = app.CreateResponse
		case len(p) == 3 && r.Method == http.MethodGet:
			c.Action = app.GetResponse
		case len(p) == 3 && r.Method == http.MethodDelete:
			c.Action = app.DeleteResponse
		case len(p) == 4 && p[3] == "input_items" && r.Method == http.MethodGet:
			c.Action = app.ListInputItems
		}
	}
	if p[1] == "conversations" {
		c.Resource = app.ConversationResource
		switch {
		case len(p) == 2 && r.Method == http.MethodPost:
			c.Action = app.CreateConversation
		case len(p) == 3 && r.Method == http.MethodGet:
			c.Action = app.GetConversation
		case len(p) == 3 && r.Method == http.MethodPost:
			c.Action = app.UpdateConversation
		case len(p) == 3 && r.Method == http.MethodDelete:
			c.Action = app.DeleteConversation
		case len(p) == 4 && p[3] == "items" && r.Method == http.MethodGet:
			c.Action = app.ListItems
		case len(p) == 4 && p[3] == "items" && r.Method == http.MethodPost:
			c.Action = app.AddItems
		case len(p) == 5 && p[3] == "items" && r.Method == http.MethodGet:
			c.Action = app.GetItem
			c.ItemID = p[4]
		case len(p) == 5 && p[3] == "items" && r.Method == http.MethodDelete:
			c.Action = app.DeleteItem
			c.ItemID = p[4]
		}
	}
	c.Page = app.PageQuery{Limit: 20, Order: "desc"}
	for key, values := range r.URL.Query() {
		if len(values) != 1 || (key != "after" && key != "limit" && key != "order") {
			c.Page.Invalid = true
		}
	}
	query := r.URL.Query()
	c.Page.After = query.Get("after")
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			c.Page.Invalid = true
		} else {
			c.Page.Limit = n
		}
	}
	if order := query.Get("order"); order != "" {
		if order != "asc" && order != "desc" {
			c.Page.Invalid = true
		}
		c.Page.Order = order
	}
	if raw := r.Header.Get("If-Match"); raw != "" {
		if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			c.InvalidVersion = true
		} else {
			n, err := strconv.ParseUint(raw[1:len(raw)-1], 10, 64)
			if err != nil || raw != `"`+strconv.FormatUint(n, 10)+`"` {
				c.InvalidVersion = true
			} else {
				c.ExpectedVersion = &n
			}
		}
	}
	return c
}

type delivery struct {
	execution.DeadlineOutput
	writer http.ResponseWriter
}

func (d *delivery) Annotate(m app.Metadata) {
	if m.GenerationID != "" {
		d.writer.Header().Set("X-TXAPI-Generation-ID", m.GenerationID)
	}
	if m.ResponseID != "" {
		d.writer.Header().Set("X-TXAPI-Response-ID", m.ResponseID)
	}
	if m.Idempotency != "" {
		d.writer.Header().Set("X-TXAPI-Idempotency", m.Idempotency)
	}
	if m.RetryAfterSeconds > 0 {
		d.writer.Header().Set("Retry-After", strconv.Itoa(m.RetryAfterSeconds))
	}
	if m.Version > 0 {
		d.writer.Header().Set("ETag", `"`+strconv.FormatUint(m.Version, 10)+`"`)
	}
	if m.NoStore {
		d.writer.Header().Set("Cache-Control", "no-store")
	}
}
func (d *delivery) Problem(p app.Problem) {
	d.Annotate(p.Metadata)
	status := 500
	switch p.Code {
	case "invalid_request_error":
		status = 400
	case "unauthorized":
		status = 401
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "idempotency_conflict", "idempotency_result_unavailable", "idempotency_in_progress", "idempotency_unknown", "history_conflict", "history_context_unavailable", "request_closed":
		status = 409
	case "request_too_large":
		status = 413
	case "rate_limit_error", "responses_capacity", "history_capacity":
		status = 429
	case "authorization_unavailable", "history_unavailable", "responses_unavailable", "archive_unavailable":
		status = 503
	}
	d.writer.Header().Set("Content-Type", "application/json")
	d.writer.WriteHeader(status)
	_ = json.NewEncoder(d.writer).Encode(map[string]any{"error": map[string]string{"type": p.Code, "message": p.Message}})
}
func (d *delivery) Document(document app.Document) {
	d.writer.Header().Set("Content-Type", "application/json")
	d.writer.Header().Set("Cache-Control", "no-store")
	var value any
	switch document.Kind {
	case app.NativeDocument:
		value = document.Raw
	case app.ConversationDocument:
		c := document.Conversation
		value = map[string]any{"id": c.ID, "object": "conversation", "created_at": c.CreatedAt / 1000, "metadata": c.Metadata}
	case app.ItemsDocument:
		p := document.Page
		value = map[string]any{"object": "list", "data": p.Items, "first_id": p.FirstID, "last_id": p.LastID, "has_more": p.HasMore}
	case app.DeletedResponseDocument, app.DeletedConversationDocument:
		value = map[string]any{"id": document.ID, "object": string(document.Kind), "deleted": true}
	default:
		d.Problem(app.Problem{Code: "internal_error", Message: "Responses document is unavailable"})
		return
	}
	_ = json.NewEncoder(d.writer).Encode(value)
}
