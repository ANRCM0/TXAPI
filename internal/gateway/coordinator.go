package gateway

import (
	"context"
	"net/http"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
)

// Execution keeps operation payloads intact; it does not translate them to Chat.
// Context must contain the principal verified by the caller's authentication boundary.
type Execution struct {
	Operation protocol.Operation
	Body      []byte
	SessionID string
}

// ExecuteTo owns one generation, including admission, attempts and finalization.
// Result and decoder state are fresh even when callers reuse a parent context.
func (g *Gateway) ExecuteTo(ctx context.Context, output core.Output, in Execution) RequestResult {
	return g.executeTo(ctx, output, in, nil)
}

// ExecuteClaimedTo consumes an authenticated durable intent through the same
// authorization/admission/routing/attempt/finalization chain as HTTP. The caller
// refreshes the Principal and owns lease renewal, output and shutdown joining.
func (g *Gateway) ExecuteClaimedTo(ctx context.Context, output core.Output, in Execution, claim model.GenerationClaim) RequestResult {
	return g.executeTo(ctx, output, in, &claim)
}

func (g *Gateway) executeTo(ctx context.Context, output core.Output, in Execution, claim *model.GenerationClaim) RequestResult {
	ctx = access.NewGenerationContext(ctx)
	result := newRequestResult(ctx)
	result.Operation = in.Operation
	if output == nil {
		result.ErrorMessage = "missing execution output"
		result.Error = &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: result.ErrorMessage, Type: "internal_error"}}
		return *result
	}
	if state := output.State(); state.Committed {
		result.Committed, result.HTTPStatus = state.Committed, state.HTTPStatus
		result.ErrorMessage = "execution output is already committed"
		result.Error = &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: result.ErrorMessage, Type: "internal_error"}}
		return *result
	}
	c := newOutputScope(ctx, output, result, in.SessionID)
	c.claim = claim
	if claim != nil && g.billingMode != billing.ModeStrict {
		writeError(c, http.StatusServiceUnavailable, "durable execution requires strict billing", "billing_unavailable")
		return *result
	}
	principal, ok := access.PrincipalFromContext(ctx)
	if !ok || principal.UserID == 0 || principal.TokenID == 0 {
		writeError(c, http.StatusUnauthorized, "a verified generation identity is required", "unauthorized")
		return *result
	}
	g.execute(c, in)
	return *result
}

func (g *Gateway) execute(c *executionScope, in Execution) {
	defer c.syncOutput()
	defer func() {
		notDispatched := c.result.Phase == core.NotDispatched
		for _, attempt := range c.result.Attempts {
			notDispatched = notDispatched && attempt.Phase == core.NotDispatched
		}
		if notDispatched {
			access.FinishTokens(c.ctx, g.limiter, 0)
		}
	}()
	c.result.Operation = in.Operation
	switch in.Operation {
	case protocol.Chat, protocol.Embeddings:
		request, err := protocol.ParseRequest(in.Operation, in.Body)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		g.executeScope(c, request)
	default:
		writeError(c, http.StatusBadRequest, "unsupported operation", "unsupported_operation")
	}
}

type executionScope struct {
	ctx             context.Context
	output          *executionOutput
	result          *RequestResult
	sessionID       string
	requestAdmitted bool
	claim           *model.GenerationClaim
}

func newOutputScope(ctx context.Context, output core.Output, result *RequestResult, sessionID string) *executionScope {
	return &executionScope{ctx: ctx, output: &executionOutput{target: output}, result: result, sessionID: sessionID}
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

func (c *executionScope) syncOutput() {
	state := c.output.State()
	c.result.Committed, c.result.HTTPStatus = state.Committed, state.HTTPStatus
}
