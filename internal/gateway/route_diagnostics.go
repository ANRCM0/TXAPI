package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"minigateway/internal/core"
	routes "minigateway/internal/foundation/contracts/routes/v1"
)

const (
	routeCapacity   = 512
	routeRetention  = 15 * time.Minute
	routeSelections = 16
	routeAttempts   = 11
)

type routeCapture struct {
	started    time.Time
	model      string
	selections []routes.Selection
	truncated  bool
}
type routeRecord struct {
	trace   routes.Trace
	expires time.Time
}
type routeHistory struct {
	mu      sync.Mutex
	records [routeCapacity]routeRecord
	next    int
	now     func() time.Time
}
type routeReader struct{ history *routeHistory }

// Diagnostics returns a read capability; the application owns session authority.
func (g *Gateway) Diagnostics() routes.Reader { return routeReader{g.routeHistory} }

func (g *Gateway) beginRouteCapture(c *executionScope) func(bool) {
	if g.routeHistory == nil {
		return func(bool) {}
	}
	capture := &routeCapture{started: time.Now()}
	c.routeCapture = capture
	return func(returned bool) {
		g.routeHistory.record(c.result, capture, returned)
	}
}

func (c *routeCapture) selectTarget(d routeDecision, duration time.Duration) {
	if len(c.selections) == routeSelections {
		c.truncated = true
		return
	}
	c.selections = append(c.selections, routes.Selection{
		Outcome: d.outcome, Picker: d.picker, CandidateChannels: d.candidateChannels,
		EligibleChannels: d.eligibleChannels, SelectionPoolChannels: d.selectionPoolChannels,
		TotalCredentials: d.totalCredentials, TriedCredentials: d.triedCredentials,
		UnavailableCredentials: d.unavailableCredentials, EligibleCredentials: d.eligibleCredentials,
		PriorityFallback: d.priorityFallback, DurationMicros: max(0, duration.Microseconds()),
		ChannelID: uint(max(0, d.selectedChannel)), CredentialID: d.selectedCredential,
	})
}

func boundedRouteText(value string, limit int, truncated *bool) string {
	if !utf8.ValidString(value) {
		*truncated = true
	}
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
		*truncated = true
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			*truncated = true
			return '?'
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
		*truncated = true
	}
	return strings.Clone(value)
}

func routeRequestIDValid(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func routePhase(phase core.Phase) string {
	switch phase {
	case core.NotDispatched, core.Dispatching, core.ResponseReceived:
		return string(phase)
	default:
		return "unknown"
	}
}

func routeTermination(termination core.Termination) string {
	switch termination {
	case core.Success, core.UpstreamError, core.ClientWriteError, core.Canceled, core.Deadline:
		return string(termination)
	default:
		return "unknown"
	}
}

func routeReason(result *RequestResult, returned bool) string {
	switch {
	case !returned:
		return "scope_interrupted"
	case result.TimeoutStage != "":
		return "timeout_" + string(result.TimeoutStage)
	case result.Termination == core.ClientWriteError:
		return "delivery_failed"
	case result.Termination == core.Canceled:
		return "canceled"
	case result.Termination == core.Deadline:
		return "deadline"
	case result.nativeFailed:
		return "upstream_failed"
	}
	if result.Error != nil {
		switch result.Error.Error.Type {
		case "no_channel", "continuation_target_unavailable":
			return "target_unavailable"
		case "invalid_request_error", "unsupported_operation", "unsupported_billing_dimension":
			return "request_rejected"
		case "forbidden":
			return "permission_denied"
		case "quota_exceeded":
			return "quota_exceeded"
		}
		if strings.HasPrefix(result.Error.Error.Type, "billing_") || strings.HasPrefix(result.Error.Error.Type, "provider_") {
			return "billing_refused"
		}
	}
	if result.StatusCode == 429 {
		return "admission_refused"
	}
	if result.StatusCode >= 200 && result.StatusCode < 300 && result.Termination == core.Success {
		return "completed"
	}
	return "failed"
}

func (h *routeHistory) record(result *RequestResult, capture *routeCapture, returned bool) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return // Observation failure cannot change inference or financial state.
	}
	trace := routes.Trace{
		ID: "rt_" + hex.EncodeToString(id[:]), UserID: result.Identity.UserID, TokenID: result.Identity.TokenID,
		StartedAt: capture.started.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		Status: result.StatusCode, HTTPStatus: result.HTTPStatus, Reason: routeReason(result, returned),
		Phase: routePhase(result.Phase), Termination: routeTermination(result.Termination),
		Committed: result.Committed, ProtocolComplete: result.ProtocolComplete, Interrupted: !returned,
		Truncated: capture.truncated, Selections: append([]routes.Selection{}, capture.selections...),
		Attempts: make([]routes.Attempt, 0, min(routeAttempts, len(result.Attempts))),
	}
	if routeRequestIDValid(result.Identity.RequestID) {
		trace.RequestID = strings.Clone(result.Identity.RequestID)
	} else if result.Identity.RequestID != "" {
		trace.Truncated = true
	}
	trace.Operation = boundedRouteText(string(result.Operation), 64, &trace.Truncated)
	trace.Model = boundedRouteText(capture.model, 128, &trace.Truncated)
	for i, attempt := range result.Attempts {
		if i == routeAttempts {
			trace.Truncated = true
			break
		}
		trace.Attempts = append(trace.Attempts, routes.Attempt{
			Number: i + 1, Status: attempt.StatusCode, HTTPStatus: attempt.HTTPStatus,
			Phase: routePhase(attempt.Phase), Termination: routeTermination(attempt.Termination),
			Committed: attempt.Committed, ProtocolComplete: attempt.ProtocolComplete,
			ChannelID: attempt.ChannelID, CredentialID: attempt.CredentialID,
			ActualModel: boundedRouteText(attempt.Model, 128, &trace.Truncated),
		})
	}
	if !returned {
		trace.Phase, trace.Termination = "unknown", "unknown"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.prune(now)
	h.records[h.next] = routeRecord{trace: trace, expires: now.Add(routeRetention)}
	h.next = (h.next + 1) % routeCapacity
}

func (h *routeHistory) prune(now time.Time) {
	for i := range h.records {
		if h.records[i].trace.ID != "" && !h.records[i].expires.After(now) {
			h.records[i] = routeRecord{}
		}
	}
}

func routeQueryValid(scope routes.Scope, q *routes.Query) bool {
	if q.Limit == 0 {
		q.Limit = 50
	}
	return scope.UserID != 0 && q.Limit > 0 && q.Limit <= 100 && q.Offset >= 0 && q.Offset <= 10000 &&
		(scope.Administrator || q.OwnerID == 0) && (q.RequestID == "" || routeRequestIDValid(q.RequestID))
}

func routeVisible(trace routes.Trace, scope routes.Scope, q routes.Query) bool {
	return trace.ID != "" && (scope.Administrator || trace.UserID == scope.UserID) &&
		(q.OwnerID == 0 || trace.UserID == q.OwnerID) && (q.TokenID == 0 || trace.TokenID == q.TokenID) &&
		(q.RequestID == "" || trace.RequestID == q.RequestID)
}

func routeProjection(trace routes.Trace, admin bool) routes.Trace {
	trace.Selections = append([]routes.Selection{}, trace.Selections...)
	trace.Attempts = append([]routes.Attempt{}, trace.Attempts...)
	if !admin {
		for i := range trace.Selections {
			trace.Selections[i].ChannelID, trace.Selections[i].CredentialID = 0, 0
		}
		for i := range trace.Attempts {
			trace.Attempts[i].ChannelID, trace.Attempts[i].CredentialID = 0, 0
			trace.Attempts[i].ActualModel = ""
		}
	}
	return trace
}

func (r routeReader) List(ctx context.Context, scope routes.Scope, q routes.Query) (routes.Page, error) {
	if ctx == nil || !routeQueryValid(scope, &q) {
		return routes.Page{}, routes.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return routes.Page{}, err
	}
	out := routes.Page{Data: []routes.Trace{}, Enabled: r.history != nil, Capacity: routeCapacity, RetentionSeconds: int(routeRetention / time.Second)}
	if r.history == nil {
		return out, nil
	}
	h := r.history
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return routes.Page{}, err
	}
	h.prune(h.now())
	matched := 0
	for i := 0; i < routeCapacity; i++ {
		trace := h.records[(h.next-1-i+routeCapacity)%routeCapacity].trace
		if !routeVisible(trace, scope, q) {
			continue
		}
		matched++
		if matched <= q.Offset {
			continue
		}
		if len(out.Data) == q.Limit {
			out.HasMore = true
			break
		}
		out.Data = append(out.Data, routeProjection(trace, scope.Administrator))
	}
	if err := ctx.Err(); err != nil {
		return routes.Page{}, err
	}
	return out, nil
}

func (r routeReader) Read(ctx context.Context, scope routes.Scope, id string) (routes.Trace, error) {
	if ctx == nil || scope.UserID == 0 {
		return routes.Trace{}, routes.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return routes.Trace{}, err
	}
	if len(id) != 35 || !strings.HasPrefix(id, "rt_") || r.history == nil {
		return routes.Trace{}, routes.ErrNotFound
	}
	if _, err := hex.DecodeString(id[3:]); err != nil {
		return routes.Trace{}, routes.ErrNotFound
	}
	h := r.history
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return routes.Trace{}, err
	}
	h.prune(h.now())
	for _, entry := range h.records {
		if entry.trace.ID == id && routeVisible(entry.trace, scope, routes.Query{}) {
			return routeProjection(entry.trace, scope.Administrator), nil
		}
	}
	return routes.Trace{}, routes.ErrNotFound
}

var _ routes.Reader = routeReader{}
