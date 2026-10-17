package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/meta"
)

// Execution keeps operation payloads intact; it does not translate them to Chat.
// Context must contain the principal verified by the caller's authentication boundary.
type Execution struct {
	Operation protocol.Operation
	Body      []byte
	SessionID string
}

// Execute owns one generation, including admission, attempts and finalization.
// Result and decoder state are fresh even when callers reuse a parent context.
func (g *Gateway) Execute(ctx context.Context, w http.ResponseWriter, in Execution) RequestResult {
	ctx = access.NewGenerationContext(ctx)
	result := newRequestResult(ctx)
	c := newExecutionScope(ctx, w, result, in.SessionID)
	principal, ok := access.PrincipalFromContext(ctx)
	if !ok || principal.UserID == 0 || principal.TokenID == 0 {
		writeError(c, http.StatusUnauthorized, "a verified generation identity is required", "unauthorized")
		return *result
	}
	g.execute(c, in)
	return *result
}

func (g *Gateway) execute(c *executionScope, in Execution) {
	defer func() {
		notDispatched := c.result.Phase == core.NotDispatched
		for _, attempt := range c.result.Attempts {
			notDispatched = notDispatched && attempt.Phase == core.NotDispatched
		}
		if notDispatched {
			access.FinishTokens(c.ctx, g.limiter, 0)
		}
	}()
	c.operation = in.Operation
	switch in.Operation {
	case protocol.Chat:
		g.executeScope(c, in.Body, meta.ModeChatCompletions)
	case protocol.Embeddings:
		g.executeScope(c, in.Body, meta.ModeEmbeddings)
	default:
		writeError(c, http.StatusBadRequest, "unsupported operation", "unsupported_operation")
	}
}

type executionScope struct {
	ctx             context.Context
	writer          *executionWriter
	result          *RequestResult
	sessionID       string
	operation       protocol.Operation
	requestAdmitted bool
}

func newExecutionScope(ctx context.Context, w http.ResponseWriter, result *RequestResult, sessionID string) *executionScope {
	return &executionScope{ctx: ctx, writer: &executionWriter{ResponseWriter: w}, result: result, sessionID: sessionID}
}

func newRequestResult(ctx context.Context) *RequestResult {
	identity := Identity{RequestID: access.RequestID(ctx)}
	if p, ok := access.PrincipalFromContext(ctx); ok {
		identity.UserID, identity.TokenID, identity.TokenName = p.UserID, p.TokenID, p.TokenName
		identity.AllowedModels = append([]string(nil), p.Policy.Models...)
		policy := p.Policy
		identity.Policy = &policy
	}
	return &RequestResult{Identity: identity, Result: core.Result{StatusCode: 500, Phase: core.NotDispatched}}
}

type executionWriter struct {
	http.ResponseWriter
	status       int
	capture      bytes.Buffer
	captureLimit int
}

func (w *executionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *executionWriter) Written() bool               { return w.status != 0 }
func (w *executionWriter) Status() int                 { return w.status }
func (w *executionWriter) WriteHeader(status int) {
	if w.Written() {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *executionWriter) Write(b []byte) (int, error) {
	if !w.Written() {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	if room := w.captureLimit - w.capture.Len(); room > 0 {
		if room > n {
			room = n
		}
		_, _ = w.capture.Write(b[:room])
	}
	return n, err
}
func (w *executionWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *executionWriter) FlushError() error {
	if !w.Written() {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

var _ io.StringWriter = (*executionWriter)(nil)
