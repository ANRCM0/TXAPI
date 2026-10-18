package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	app "minigateway/internal/foundation/contracts/responses/v1"
	appv2 "minigateway/internal/foundation/contracts/responses/v2"
)

type serviceV2 struct{ service *Service }
type callV2 struct{ call *requestCall }
type ownedPayload []byte

func (p ownedPayload) Read() ([]byte, error) { return bytes.Clone(p), nil }

func (s *Service) V2() appv2.Service { return serviceV2{s} }
func (s serviceV2) Begin(ctx context.Context, action app.Action) (appv2.Call, *app.Problem) {
	call, p := s.service.Begin(ctx, action)
	if p != nil {
		return nil, p
	}
	return &callV2{call: call.(*requestCall)}, nil
}
func (c *callV2) Close() { c.call.Close() }
func (c *callV2) Handle(command appv2.Command, delivery app.Delivery) {
	call := c.call
	call.mu.Lock()
	defer call.mu.Unlock()
	if missing(delivery) {
		return
	}
	if call.used || call.closed || command.Base.Action != call.action {
		problem(delivery, "request_closed", "this request scope cannot be reused")
		return
	}
	call.used = true
	base := command.Base
	if command.StartingAfter != nil {
		n := *command.StartingAfter
		command.StartingAfter = &n
	}
	base.IdempotencyKeys = append([]string(nil), base.IdempotencyKeys...)
	if base.ExpectedVersion != nil {
		n := *base.ExpectedVersion
		base.ExpectedVersion = &n
	}
	s := call.service
	if base.Action == app.CreateResponse {
		body, e := readPayload(base)
		if e != nil {
			problem(delivery, "invalid_request_error", "could not read Responses body")
			return
		}
		if len(body) > MaxRequestBytes {
			problem(delivery, "request_too_large", "Responses request exceeds 1 MiB")
			return
		}
		var fields map[string]json.RawMessage
		var flags struct {
			Background bool
			Stream     bool
			Store      *bool
		}
		parsed := json.Unmarshal(body, &fields) == nil
		if parsed {
			_ = json.Unmarshal(fields["background"], &flags.Background)
			_ = json.Unmarshal(fields["stream"], &flags.Stream)
			if raw, ok := fields["store"]; ok {
				var value bool
				if json.Unmarshal(raw, &value) == nil {
					flags.Store = &value
				}
			}
		}
		// The original parser remains authoritative for malformed foreground
		// input; a true background flag selects exactly the new task owner.
		if parsed && flags.Background {
			if base.HasQuery || base.InvalidVersion || base.ExpectedVersion != nil || command.Stream || command.StartingAfter != nil {
				problem(delivery, "invalid_request_error", "unsupported background create options")
				return
			}
			if missing(s.background) {
				backgroundProblem(delivery, bg.ErrUnsupportedCapability)
				return
			}
			key := ""
			if len(base.IdempotencyKeys) > 1 || len(base.IdempotencyKeys) == 1 && !validKey(base.IdempotencyKeys[0]) {
				problem(delivery, "invalid_request_error", "Idempotency-Key must contain 1 to 256 visible ASCII bytes")
				return
			}
			if len(base.IdempotencyKeys) == 1 {
				key = base.IdempotencyKeys[0]
			}
			id, e := randomID()
			if e != nil {
				backgroundProblem(delivery, bg.ErrUnavailable)
				return
			}
			receipt, e := s.background.Submit(call.ctx, bg.Submit{ID: "resp_" + id, Operation: execution.Responses, Body: body, IdempotencyKey: key, Stream: flags.Stream, Store: flags.Store == nil || *flags.Store})
			if e != nil {
				backgroundProblem(delivery, e)
				return
			}
			if flags.Stream {
				s.observeBackground(call.ctx, delivery, call.binding.UserID, receipt, nil)
			} else {
				deliverBackground(call.ctx, delivery, receipt)
			}
			return
		}
		base.Payload = ownedPayload(body)
		s.create(call.ctx, delivery, call.principal, call.binding, base)
		return
	}
	if base.Resource == app.ResponseResource && (base.Action == app.GetResponse || base.Action == app.DeleteResponse || base.Action == appv2.CancelResponse || base.Action == app.ListInputItems) && !missing(s.resources) {
		kind, e := s.resources.Kind(call.ctx, base.ID)
		if e != nil {
			backgroundProblem(delivery, e)
			return
		}
		if kind == bg.BackgroundResource {
			if base.Page.Invalid || base.InvalidVersion || base.Action != app.GetResponse && base.HasQuery || base.Action == appv2.CancelResponse && base.ExpectedVersion != nil {
				problem(delivery, "invalid_request_error", "unsupported background resource options")
				return
			}
			if command.StartingAfter != nil && !command.Stream {
				problem(delivery, "invalid_request_error", "starting_after requires stream")
				return
			}
			var receipt bg.Receipt
			switch base.Action {
			case app.GetResponse:
				receipt, e = s.background.Read(call.ctx, base.ID)
			case appv2.CancelResponse:
				receipt, e = s.background.Cancel(call.ctx, base.ID)
			case app.DeleteResponse:
				e = s.resources.Delete(call.ctx, base.ID, base.ExpectedVersion)
				if e == nil {
					delivery.Annotate(app.Metadata{NoStore: true})
					delivery.Document(app.Document{Kind: app.DeletedResponseDocument, ID: base.ID})
					return
				}
			default:
				e = bg.ErrUnsupportedCapability
			}
			if e != nil {
				backgroundProblem(delivery, e)
				return
			}
			if base.Action == app.GetResponse && command.Stream {
				s.observeBackground(call.ctx, delivery, call.binding.UserID, receipt, command.StartingAfter)
			} else {
				deliverBackground(call.ctx, delivery, receipt)
			}
			return
		}
		if kind != bg.ForegroundResource || base.Action == appv2.CancelResponse {
			backgroundProblem(delivery, bg.ErrNotFound)
			return
		}
	}
	if base.Action == appv2.CancelResponse {
		backgroundProblem(delivery, bg.ErrUnsupportedCapability)
		return
	}
	s.historyCommand(call.ctx, delivery, base, call.binding)
}

func backgroundProblem(delivery app.Delivery, e error) {
	p := app.Problem{Code: "background_unavailable", Message: "background response is temporarily unavailable", Metadata: app.Metadata{NoStore: true}}
	switch {
	case errors.Is(e, bg.ErrNotFound):
		p.Code = "not_found"
		p.Message = "response not found"
	case errors.Is(e, bg.ErrUnauthorized):
		p.Code = "unauthorized"
		p.Message = "a current identity is required"
	case errors.Is(e, bg.ErrConflict):
		p.Code = "background_conflict"
		p.Message = "request conflicts with current background state"
	case errors.Is(e, bg.ErrUncertain):
		p.Code = "background_unknown"
		p.Message = "remote execution may have started; do not resubmit"
	case errors.Is(e, bg.ErrCursorUnavailable):
		p.Code = "background_result_unavailable"
		p.Message = "temporary background result or cursor is unavailable"
	case errors.Is(e, bg.ErrCapacity):
		p.Code = "background_capacity"
		p.Message = "background capacity is busy"
		p.Metadata.RetryAfterSeconds = 1
	case errors.Is(e, bg.ErrUnsupportedBilling):
		p.Code = "unsupported_background_billing"
		p.Message = "background creation requires disabled billing"
	case errors.Is(e, bg.ErrUnsupportedCapability):
		p.Code = "unsupported_background"
		p.Message = "native background capability is not enabled for this request"
	}
	delivery.Problem(p)
}

func deliverBackground(ctx context.Context, delivery app.Delivery, r bg.Receipt) {
	if r.Stage == bg.StageUncertain {
		backgroundProblem(delivery, bg.ErrUncertain)
		return
	}
	body := bytes.Clone(r.Document)
	if len(body) == 0 {
		status := "queued"
		if r.Stage == bg.StageTerminal {
			status = "failed"
			if r.CancelRequested {
				status = "cancelled"
			}
		}
		body, _ = json.Marshal(struct {
			ID         string            `json:"id"`
			Object     string            `json:"object"`
			CreatedAt  int64             `json:"created_at"`
			Model      string            `json:"model"`
			Status     string            `json:"status"`
			Background bool              `json:"background"`
			Output     []json.RawMessage `json:"output"`
		}{r.ID, "response", r.CreatedAt.Unix(), r.Model, status, true, []json.RawMessage{}})
	}
	delivery.Annotate(app.Metadata{ResponseID: r.ID, Version: r.Revision, NoStore: true})
	if e := delivery.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil && !errors.Is(e, execution.ErrDeadlineUnsupported) {
		backgroundProblem(delivery, bg.ErrUnavailable)
		return
	}
	defer delivery.SetWriteDeadline(time.Time{})
	if e := delivery.Commit(ctx, execution.ResponseMetadata{StatusCode: 200, ContentType: "application/json", Values: map[string]string{"Cache-Control": "no-store", "X-TXAPI-Background-Retention": "temporary", "X-TXAPI-Background-Expires-At": r.ContentExpiresAt.UTC().Format(time.RFC3339)}}); e != nil {
		return
	}
	_, _ = delivery.Write(ctx, body)
}

var _ appv2.Service = serviceV2{}

func (s *Service) observeBackground(parent context.Context, delivery app.Delivery, owner uint, r bg.Receipt, after *uint64) {
	ctx, cancel := context.WithDeadline(parent, minTime(r.ContentExpiresAt, time.Now().Add(5*time.Minute)))
	defer cancel()
	defer delivery.SetWriteDeadline(time.Time{})
	committed := false
	for ctx.Err() == nil {
		if !s.allowHistoryRead(owner) {
			if !committed {
				problem(delivery, "rate_limit_error", "background observation rate exceeded")
			}
			return
		}
		page, e := s.background.Events(ctx, r.ID, bg.EventQuery{After: after, Limit: 64})
		if e != nil {
			if !committed {
				backgroundProblem(delivery, e)
			}
			return
		}
		if len(page.Events) > 0 || page.Terminal {
			if e := delivery.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil && !errors.Is(e, execution.ErrDeadlineUnsupported) {
				if !committed {
					backgroundProblem(delivery, bg.ErrUnavailable)
				}
				return
			}
			if !committed {
				delivery.Annotate(app.Metadata{ResponseID: r.ID, NoStore: true})
				e = delivery.Commit(ctx, execution.ResponseMetadata{StatusCode: 200, ContentType: "text/event-stream", Values: map[string]string{"Cache-Control": "no-store", "X-TXAPI-Background-Retention": "temporary", "X-TXAPI-Background-Expires-At": r.ContentExpiresAt.UTC().Format(time.RFC3339)}})
				if e != nil {
					return
				}
				committed = true
			}
			for _, event := range page.Events {
				if e := delivery.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil && !errors.Is(e, execution.ErrDeadlineUnsupported) {
					return
				}
				n, e := delivery.Write(ctx, event.Data)
				if e != nil || n != len(event.Data) {
					return
				}
				if delivery.Flush(ctx) != nil {
					return
				}
			}
			after = page.Next
			if page.Terminal {
				return
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			if !committed {
				backgroundObservationExpired(delivery, r.ContentExpiresAt)
			}
			return
		case <-timer.C:
		}
	}
	if !committed {
		backgroundObservationExpired(delivery, r.ContentExpiresAt)
	}
}

func backgroundObservationExpired(delivery app.Delivery, expiry time.Time) {
	if !expiry.After(time.Now()) {
		backgroundProblem(delivery, bg.ErrCursorUnavailable)
	} else {
		backgroundProblem(delivery, bg.ErrUnavailable)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
