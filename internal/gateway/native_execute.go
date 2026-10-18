package gateway

import (
	"context"
	"errors"
	"math"

	"minigateway/internal/access"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	textvalue "minigateway/internal/foundation/contracts/text/v1"
)

func (n *nativeSession) Execute(parent context.Context, in contract.Create, deliver v2.FrameDelivery) (result contract.Result) {
	result.Facts = v2.ExchangeResult{Dispatch: v2.NotDispatched, Termination: v2.Failed, Usage: unknownUsageFact()}
	if parent == nil || deliver == nil {
		result.Problem = nativeProblem("invalid_request", 1008)
		return
	}
	n.mu.Lock()
	if n.closed {
		result.Problem = cloneNativeProblem(n.problem)
		if result.Problem == nil {
			result.Problem = nativeProblem("session_expired", 1000)
		}
		n.mu.Unlock()
		return
	}
	if n.active {
		result.Problem = nativeProblem("busy", 0)
		n.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, n.service.options.Limits.RoundTimeout)
	stop := context.AfterFunc(n.ctx, cancel)
	activeDone := make(chan struct{})
	n.active = true
	n.activeCancel = cancel
	n.activeDone = activeDone
	n.mu.Unlock()
	reserved, exchangeStarted, deliveryStarted := false, false, false
	estimate := 0
	gateContext := ctx
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Problem = nativeProblem("upstream_error", 1011)
			result.Facts.Err = errors.New("native websocket round panicked")
			result.Facts.Termination = v2.Failed
			if exchangeStarted {
				result.Facts.Dispatch = v2.Dispatching
				result.Facts.UpstreamWriteStarted = true
			}
			result.Facts.DeliveryCommitted = result.Facts.DeliveryCommitted || deliveryStarted
		}
		if reserved {
			actual := 0
			if result.Facts.UpstreamWriteStarted {
				actual = estimate
				if result.Facts.Usage.Completeness == v2.Complete && result.Facts.Usage.Counts.TotalTokens != nil {
					actual = int(*result.Facts.Usage.Counts.TotalTokens)
				}
			}
			func() {
				defer func() {
					if recover() != nil {
						result.Problem = nativeProblem("unavailable", 1013)
					}
				}()
				n.service.options.Gate.Finish(gateContext, actual)
			}()
		}
		stop()
		cancel()
		n.mu.Lock()
		if result.Problem != nil && result.Problem.Code != "busy" {
			if n.problem == nil {
				copy := *result.Problem
				n.problem = &copy
			}
			result.Problem = cloneNativeProblem(n.problem)
			n.closed = true
			n.cancel()
		}
		n.active = false
		n.activeCancel = nil
		close(activeDone)
		n.mu.Unlock()
	}()
	fail := func(code string, closeCode uint16, err error) contract.Result {
		result.Problem = nativeProblem(code, closeCode)
		result.Facts.Err = err
		return result
	}
	if ctx.Err() != nil {
		return fail("session_expired", 1000, ctx.Err())
	}
	ctx = access.NewGenerationContext(ctx)
	refreshed, principal, err := n.service.options.Authority.Refresh(ctx, n.binding)
	if err != nil || principal.UserID != n.binding.UserID || principal.TokenID != n.binding.TokenID {
		return fail("unauthorized", 1008, err)
	}
	ctx = refreshed
	if int64(len(in.JSON)) > n.service.options.Limits.MaxMessageBytes {
		return fail("message_too_large", 1009, nil)
	}
	create, err := n.service.options.Parse(v2.Frame{Type: 1, Data: append([]byte(nil), in.JSON...)})
	if err != nil {
		return fail("invalid_request", 1008, err)
	}
	if n.service.gateway.BillingMode() != "disabled" {
		return fail("unavailable", 1013, nil)
	}
	if !principal.Policy.Allows(create.Model) {
		return fail("unauthorized", 1008, nil)
	}
	if n.model != "" && n.model != create.Model {
		return fail("continuation_invalid", 1008, nil)
	}
	if create.PreviousID != "" && (n.latestID == "" || create.PreviousID != n.latestID) {
		return fail("continuation_invalid", 1008, nil)
	}
	estimate = textvalue.EstimateTokens(create.PromptText)
	if create.PreviousID != "" && principal.TPMLimit > 0 {
		if n.previousTotal == nil || *n.previousTotal > int64(math.MaxInt-estimate) {
			return fail("invalid_request", 1008, nil)
		}
		estimate += int(*n.previousTotal)
	}
	var decisionRejected bool
	var retry int
	gateContext, decision := n.service.options.Gate.Admit(ctx, func() int { return estimate })
	decisionRejected, retry = decision.Rejected, decision.RetryAfterSeconds
	if decisionRejected {
		result.Problem = nativeProblem("rate_limited", 1013)
		result.Problem.RetryAfterSeconds = retry
		return
	}
	ctx = gateContext
	reserved = true
	release, err := n.service.gateway.admission.Acquire(ctx, AdmissionScope{Level: RequestAdmission, Operation: v1.Responses, UserID: n.binding.UserID, TokenID: n.binding.TokenID, Model: create.Model})
	if release != nil {
		defer release()
	}
	if err != nil || release == nil {
		return fail("rate_limited", 1013, err)
	}
	wasBound := n.target.ChannelID != 0
	ch, credential, err := n.targetForRound(ctx, create.Model)
	if err != nil {
		if !wasBound {
			return fail("unavailable", 1013, err)
		}
		return fail("continuation_invalid", 1008, err)
	}
	attempt, err := n.service.gateway.admission.Acquire(ctx, AdmissionScope{Level: AttemptAdmission, Operation: v1.Responses, UserID: n.binding.UserID, TokenID: n.binding.TokenID, Model: create.Model, ChannelID: uint(ch.id), CredentialID: credential.ID, Attempt: 1})
	if attempt != nil {
		defer attempt()
	}
	if err != nil || attempt == nil {
		return fail("rate_limited", 1013, err)
	}
	initial, err := n.service.options.Encode(create.Initial, n.target.ActualModel)
	if err != nil {
		return fail("invalid_request", 1008, err)
	}
	if nilInterface(n.connection) {
		n.connection, err = n.generation.transport.Dial(ctx, credential.APIKey)
		if err != nil || nilInterface(n.connection) {
			return fail("upstream_error", 1011, err)
		}
	}
	decoder := n.service.options.Decoder(n.target.ActualModel)
	if decoder == nil {
		return fail("upstream_error", 1011, errors.New("native websocket decoder unavailable"))
	}
	var native v1.NativeCompletion
	exchangeStarted = true
	result.Facts = (WebSocketExchangeExecutor{}).ExecuteExchange(ctx, v2.Exchange{Upstream: n.connection, Initial: initial,
		Decode: func(frame v2.Frame) (v2.Envelope, error) {
			envelope, err := decoder(frame)
			if err == nil && envelope.Native != nil {
				native = *envelope.Native
			}
			return envelope, err
		},
		Deliver: func(ctx context.Context, frame v2.Frame) (int, error) {
			deliveryStarted = true
			return deliver(ctx, frame)
		},
	})
	if result.Facts.Termination != v2.Success || !result.Facts.ProtocolComplete || native.Status != "completed" || native.ID == "" || len(native.ID) > 256 {
		return fail("upstream_error", 1011, result.Facts.Err)
	}
	n.latestID = native.ID
	n.previousTotal = nil
	if result.Facts.Usage.Completeness == v2.Complete && result.Facts.Usage.Counts.TotalTokens != nil {
		count := *result.Facts.Usage.Counts.TotalTokens
		n.previousTotal = &count
	}
	return
}
