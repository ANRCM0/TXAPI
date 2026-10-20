package gateway

import (
	"context"
	"net/http"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/model"
)

// Execution keeps operation payloads intact; it does not translate them to Chat.
// Context must contain the principal verified by the caller's authentication boundary.
type Execution struct {
	Operation      execution.Operation
	Body           []byte
	SessionID      string
	ResponseID     string
	ResponseTarget *ResponseTarget
}

// ExecuteTo owns one generation, including admission, attempts and finalization.
// Result and decoder state are fresh even when callers reuse a parent context.
func (g *Gateway) ExecuteTo(ctx context.Context, output core.Output, in Execution) RequestResult {
	return g.executeFrom(ctx, output, in, nil, false, invocationForeground)
}

// ExecuteClaimedTo consumes an authenticated durable intent through the same
// authorization/admission/routing/attempt/finalization chain as HTTP. The caller
// refreshes the Principal and owns lease renewal, output and shutdown joining.
func (g *Gateway) ExecuteClaimedTo(ctx context.Context, output core.Output, in Execution, claim model.GenerationClaim) RequestResult {
	return g.executeFrom(ctx, output, in, &claim, false, invocationClaimed)
}

// executeTo is the foreground adapter; claimed work retains its separate source.
func (g *Gateway) executeTo(ctx context.Context, output core.Output, in Execution, claim *model.GenerationClaim, httpAdmission bool) RequestResult {
	source := invocationForeground
	if claim != nil {
		source = invocationClaimed
	}
	return g.executeFrom(ctx, output, in, claim, httpAdmission, source)
}

func (g *Gateway) executeFrom(ctx context.Context, output core.Output, in Execution, claim *model.GenerationClaim, httpAdmission bool, source invocationSource) RequestResult {
	if httpAdmission {
		ctx = access.NewHTTPGenerationContext(ctx)
	} else {
		ctx = access.NewGenerationContext(ctx)
	}
	result := newRequestResult(ctx)
	result.Operation = in.Operation
	if nilInterface(output) {
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
	c.source = source
	c.responseID = in.ResponseID
	if in.ResponseTarget != nil {
		copy := *in.ResponseTarget
		c.responseTarget = &copy
	}
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
	returned := false
	if httpAdmission {
		finishRoutes := g.beginRouteCapture(c)
		defer func() { finishRoutes(returned) }()
	}
	g.execute(c, in)
	returned = true
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
	if _, known := g.operations[in.Operation]; known && in.Operation != execution.Chat && in.Operation != execution.Embeddings && (c.claim != nil || g.billingMode == billing.ModeStrict) {
		message := g.operations[in.Operation].Errors.StrictDenied
		if message == "" {
			message = "operation has no verified strict billing dimension"
		}
		writeError(c, http.StatusBadRequest, message, "unsupported_billing_dimension")
		return
	}
	request, err := g.ParseRequest(in.Operation, in.Body)
	if err != nil {
		kind := "invalid_request_error"
		if _, known := g.operations[in.Operation]; !known {
			kind = "unsupported_operation"
		}
		writeError(c, http.StatusBadRequest, err.Error(), kind)
		return
	}
	if g.billingMode != billing.ModeDisabled && hasHostedSearch(request) {
		writeError(c, http.StatusBadRequest, "Responses hosted search requires disabled billing", "unsupported_billing_dimension")
		return
	}
	if g.billingMode != billing.ModeDisabled && hasCompaction(request) {
		writeError(c, http.StatusBadRequest, "Responses compaction requires disabled billing", "unsupported_billing_dimension")
		return
	}
	g.executeScope(c, request)
}

type invocationSource uint8

const (
	invocationUnspecified invocationSource = iota
	invocationForeground
	invocationClaimed
)

type executionScope struct {
	source          invocationSource
	usageRecording  *usageRecording
	routeCapture    *routeCapture
	ctx             context.Context
	output          *executionOutput
	result          *RequestResult
	sessionID       string
	responseID      string
	responseTarget  *ResponseTarget
	managedTarget   *model.Credential
	managedIssued   *authv3.FailureWrite
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
