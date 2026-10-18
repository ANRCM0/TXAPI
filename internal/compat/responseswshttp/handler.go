// Package responseswshttp owns HTTP upgrade, wire errors and WebSocket presentation.
package responseswshttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	bridge "minigateway/internal/compat/websocket"
	execution "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	transport "minigateway/internal/transport/websocket"
)

type Handler struct {
	service   contract.Service
	client    contract.Client
	authority identity.Authority
	transport *transport.Manager
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	stopped   bool
	wg        sync.WaitGroup
	waitOnce  sync.Once
	done      chan struct{}
}

func New(ctx context.Context, service contract.Service, client contract.Client, authority identity.Authority, manager *transport.Manager) (*Handler, error) {
	if ctx == nil || service == nil || client == nil || authority == nil || manager == nil {
		return nil, errors.New("native websocket HTTP dependencies unavailable")
	}
	root, cancel := context.WithCancel(ctx)
	return &Handler{service: service, client: client, authority: authority, transport: manager, ctx: root, cancel: cancel, done: make(chan struct{})}, nil
}
func (h *Handler) Stop() { h.mu.Lock(); h.stopped = true; h.mu.Unlock() }
func (h *Handler) Close(ctx context.Context) error {
	h.Stop()
	h.waitOnce.Do(func() { go func() { h.wg.Wait(); close(h.done) }() })
	select {
	case <-h.done:
		h.cancel()
		return h.transport.Close(ctx)
	case <-ctx.Done():
		h.cancel()
		return ctx.Err()
	}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopped || h.ctx.Err() != nil {
		h.mu.Unlock()
		writeHTTPProblem(w, contract.Problem{Code: "draining"})
		return
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()
	if r.Method != http.MethodGet || r.URL.Path != "/v1/responses" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeHTTPProblem(w, contract.Problem{Code: "invalid_request"})
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		writeHTTPProblem(w, contract.Problem{Code: "unauthorized"})
		return
	}
	if err := h.transport.CheckOrigin(r); err != nil {
		writeHTTPProblem(w, contract.Problem{Code: "policy_violation"})
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeHTTPProblem(w, contract.Problem{Code: "invalid_request"})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(h.ctx, cancel)
	defer func() { stop(); cancel() }()
	principal, ok := h.authority.Current(ctx)
	if !ok {
		writeHTTPProblem(w, contract.Problem{Code: "unauthorized"})
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		writeHTTPProblem(w, contract.Problem{Code: "unavailable"})
		return
	}
	id := execution.ConnectionID(hex.EncodeToString(random))
	opening, cancelOpen := context.WithTimeout(ctx, 10*time.Second)
	session, problem := h.service.Open(opening, contract.Open{Binding: identity.Binding{UserID: principal.UserID, TokenID: principal.TokenID}, ConnectionID: id})
	cancelOpen()
	if problem != nil {
		writeHTTPProblem(w, *problem)
		return
	}
	if session == nil {
		writeHTTPProblem(w, contract.Problem{Code: "unavailable"})
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 130*time.Second)
		defer stop()
		_ = session.Close(cleanup)
	}()
	connection, err := h.transport.Accept(w, r)
	if err != nil {
		return
	}
	defer connection.Close()
	_ = h.client.Run(ctx, session, bridge.Adapt(id, connection), func(ctx context.Context, p contract.Problem) error {
		safe := normalize(p)
		body, _ := json.Marshal(map[string]any{"type": "error", "error": wireError(safe)})
		if err := connection.Write(ctx, transport.Text, body); err != nil {
			return err
		}
		if safe.CloseCode != 0 {
			return connection.CloseStatus(ctx, safe.CloseCode)
		}
		return nil
	})
}

type problemDefinition struct {
	message string
	close   uint16
	status  int
}

var definitions = map[string]problemDefinition{
	"unavailable":          {"Native WebSocket service is unavailable.", 1013, 503},
	"unauthorized":         {"The current API key is not authorized.", 1008, 401},
	"policy_violation":     {"The request origin is not allowed.", 1008, 403},
	"invalid_request":      {"The native WebSocket request is not supported.", 1008, 400},
	"unsupported_frame":    {"Only text WebSocket messages are supported.", 1003, 400},
	"message_too_large":    {"The WebSocket message exceeds the supported size.", 1009, 413},
	"busy":                 {"Another generation is still active on this connection.", 0, 409},
	"rate_limited":         {"Request or connection capacity is unavailable.", 1013, 429},
	"continuation_invalid": {"The continuation is not available on this connection.", 1008, 409},
	"upstream_error":       {"Upstream generation could not be completed.", 1011, 502},
	"session_expired":      {"The WebSocket session has ended.", 1000, 409},
	"draining":             {"The service is draining.", 1001, 503},
}

func normalize(p contract.Problem) contract.Problem {
	definition, ok := definitions[p.Code]
	if !ok {
		p.Code = "upstream_error"
		definition = definitions[p.Code]
	}
	p.Message, p.CloseCode = definition.message, definition.close
	if p.RetryAfterSeconds < 0 || p.RetryAfterSeconds > 3600 {
		p.RetryAfterSeconds = 0
	}
	return p
}
func wireError(p contract.Problem) map[string]any {
	out := map[string]any{"type": "txapi_error", "code": p.Code, "message": p.Message}
	if p.RetryAfterSeconds > 0 {
		out["retry_after_seconds"] = p.RetryAfterSeconds
	}
	return out
}
func writeHTTPProblem(w http.ResponseWriter, p contract.Problem) {
	p = normalize(p)
	if p.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(definitions[p.Code].status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": wireError(p)})
}

var _ http.Handler = (*Handler)(nil)
