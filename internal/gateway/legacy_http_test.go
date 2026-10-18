package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	"minigateway/internal/compat/generationhttp"
	"minigateway/internal/core"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/model"
)

func (g *Gateway) HandleChatCompletions(c *gin.Context) {
	generationhttp.New(testHTTPService{g, state(c)}).HandleChatCompletions(c)
}
func (g *Gateway) HandleEmbeddings(c *gin.Context) {
	generationhttp.New(testHTTPService{g, state(c)}).HandleEmbeddings(c)
}

// Execute is the HTTP compatibility adapter for the shared execution entry.
func (g *Gateway) Execute(ctx context.Context, w http.ResponseWriter, in Execution) RequestResult {
	return g.ExecuteTo(ctx, core.NewHTTPOutput(w), in)
}

func newExecutionScope(ctx context.Context, w http.ResponseWriter, result *RequestResult, sessionID string) *executionScope {
	return newOutputScope(ctx, core.NewHTTPOutput(w), result, sessionID)
}

// Relay is retained for existing in-package integrations. New consumers use Execute.
func (g *Gateway) Relay(c *gin.Context, req *model.ModelRequest, body []byte, allowed []string, mode int, timeout time.Duration) {
	scope := newExecutionScope(c.Request.Context(), c.Writer, state(c), c.GetHeader(SessionIDHeader))
	if g.billingMode == billing.ModeStrict && (!g.strictReady() || scope.result.reservation.BillingID() == "" || scope.result.strictRequest == nil) {
		writeError(scope, 503, "strict billing is unavailable; requests cannot be charged safely", "billing_unavailable")
		return
	}
	operation, known := protocol.OperationForMode(mode)
	if !known {
		writeError(scope, 400, "unsupported operation", "unsupported_operation")
		return
	}
	request, err := g.ParseRequest(operation, body)
	if err != nil || req == nil || req.Model != request.Model() || req.Stream != request.Stream() {
		writeError(scope, 400, "request body and routing metadata must match", "invalid_request_error")
		return
	}
	scope.result.Operation = operation
	g.relay(scope, request, allowed, timeout)
}

func state(c *gin.Context) *RequestResult {
	if c.Request == nil {
		c.Request = &http.Request{Header: make(http.Header)}
	}
	if result := ResultFromContext(c.Request.Context()); result != nil {
		return result
	}
	result := newRequestResult(c.Request.Context())
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), resultContextKey{}, result))
	return result
}

func relayStatus(c *gin.Context) int { return state(c).StatusCode }

// Existing test entry names delegate to the actual L2 production handlers.
type testHTTPService struct {
	gateway *Gateway
	result  *RequestResult
}

func (s testHTTPService) Execute(ctx context.Context, out execv1.Output, in execv1.Invocation) execv1.Outcome {
	result := s.gateway.executeTo(ctx, ToCoreOutput(out), invocation(in), nil, true)
	*s.result = result
	return FromGateway(result)
}
func (s testHTTPService) Models(ctx context.Context) []execv1.Model { return s.gateway.Models(ctx) }
func (g *Gateway) ListModels(c *gin.Context) {
	generationhttp.New(testHTTPService{g, state(c)}).ListModels(c)
}
