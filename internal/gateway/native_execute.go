package gateway

import (
	"context"
	"errors"
	"math"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	stored "minigateway/internal/foundation/contracts/responsehistory/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	textvalue "minigateway/internal/foundation/contracts/text/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
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
	var recording *nativeUsageRecording
	var storedRound stored.StoredRound
	var finance *nativeFinance
	observed := websocketExchangeObservation{usage: unknownDetailedUsageFact()}
	interrupted := false
	defer func() {
		if recovered := recover(); recovered != nil {
			interrupted = true
			result.Problem = nativeProblem("upstream_error", 1011)
			result.Facts.Err = errors.New("native websocket round panicked")
			result.Facts.Termination = v2.Failed
			if exchangeStarted {
				result.Facts.Dispatch = v2.Dispatching
				result.Facts.UpstreamWriteStarted = true
			}
			result.Facts.DeliveryCommitted = result.Facts.DeliveryCommitted || deliveryStarted
		}
		if storedRound != nil {
			if err := finishStoredNativeRound(storedRound, result.Facts.UpstreamWriteStarted || result.Facts.Dispatch != v2.NotDispatched); err != nil {
				result.Problem = nativeProblem("unavailable", 1013)
			}
		}
		if finance != nil {
			if err := finance.finish(ctx, observed, exchangeStarted); err != nil {
				result.Problem = nativeProblem("unavailable", 1013)
			}
		}
		recording.finish(ctx, &result, observed.usage, observed.native.Status, interrupted)
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
	if !nativeBillingAllowed(n.service.gateway) {
		return fail("unavailable", 1013, nil)
	}
	if !principal.Policy.Allows(create.Model) {
		return fail("unauthorized", 1008, nil)
	}
	if n.service.gateway.hasManagedGrant(principal.UserID, principal.TokenID, create.Model) {
		return fail("unavailable", 1013, nil)
	}
	if n.service.gateway.billingMode == billing.ModeStrict {
		// Persistent parents and connection-local provider caches have no
		// self-contained input proof in this first charged operation.
		if create.Store || create.PreviousID != "" {
			return fail("invalid_request", 1008, nil)
		}
		if _, err := billing.ParseResponsesStrictRequest(create.Initial.Data); err != nil {
			return fail("invalid_request", 1008, err)
		}
		pre, err := n.service.gateway.strictBilling.Reserve(ctx, n.binding.TokenID, n.binding.UserID, create.Model)
		if err != nil {
			return fail("unavailable", 1013, err)
		}
		finance = &nativeFinance{gateway: n.service.gateway, pre: pre}
	}
	if n.model != "" && n.model != create.Model {
		return fail("continuation_invalid", 1008, nil)
	}
	if !create.Store && create.PreviousID != "" && (n.latestID == "" || create.PreviousID != n.latestID) {
		return fail("continuation_invalid", 1008, nil)
	}
	var required *v1.TargetConstraint
	if create.Store {
		if nilInterface(n.service.options.StoredHistory) || n.service.options.EncodeStored == nil {
			return fail("invalid_request", 1008, nil)
		}
		storedRound, err = n.service.options.StoredHistory.Prepare(ctx, n.binding, stored.StoredCreate{Model: create.Model, PreviousID: create.PreviousID, JSON: create.Initial.Data})
		if err != nil || nilInterface(storedRound) {
			if create.PreviousID != "" {
				return fail("continuation_invalid", 1008, err)
			}
			return fail("unavailable", 1013, err)
		}
		prepared := storedRound.Prepared()
		create.Initial = v2.Frame{Type: 1, Data: prepared.JSON}
		create.PromptText, required = prepared.PromptText, prepared.Target
	}
	estimate = textvalue.EstimateTokens(create.PromptText)
	if !create.Store && create.PreviousID != "" && principal.TPMLimit > 0 {
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
	recording, err = n.service.gateway.beginNativeUsage(ctx, create.Model)
	if err != nil {
		result.RecordingState = "unavailable"
		return fail("unavailable", 1013, err)
	}
	if recording != nil {
		result.UsageRecordID, result.RecordingState = recording.id, "open"
	}

	wasBound := n.target.ChannelID != 0
	ch, credential, err := n.targetForRound(ctx, create.Model, required)
	if err != nil {
		if !wasBound && required == nil {
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
	encode := n.service.options.Encode
	if create.Store {
		encode = n.service.options.EncodeStored
	}
	initial, err := encode(create.Initial, n.target.ActualModel)
	if err != nil {
		return fail("invalid_request", 1008, err)
	}
	if finance != nil {
		if err := finance.start(ctx, ch, credential, initial.Data); err != nil {
			return fail("unavailable", 1013, err)
		}
	}
	if !recording.prepare(ctx, history.Target{ActualModel: n.target.ActualModel, Group: ch.groupName, Protocol: ch.typ, ChannelID: uint(ch.id), CredentialID: credential.ID}) {
		return fail("unavailable", 1013, errors.New("usage recording unavailable"))
	}

	if nilInterface(n.connection) {
		if bound, ok := n.generation.transport.(BoundNativeWSResource); ok {
			endpoint, valid := nativeWSURL(ch.baseURL)
			if !valid {
				return fail("unavailable", 1013, nil)
			}
			n.connection, err = bound.DialTarget(ctx, endpoint, credential.APIKey)
		} else {
			n.connection, err = n.generation.transport.Dial(ctx, credential.APIKey)
		}
		if err != nil || nilInterface(n.connection) {
			return fail("upstream_error", 1011, err)
		}
	}
	decoder := n.service.options.Decoder(n.target.ActualModel)
	if decoder == nil {
		return fail("upstream_error", 1011, errors.New("native websocket decoder unavailable"))
	}
	var decodeUsage func(v2.Frame) (v3.Envelope, error)
	if n.service.options.UsageDecoder != nil {
		rich := n.service.options.UsageDecoder(n.target.ActualModel)
		if rich == nil {
			return fail("upstream_error", 1011, errors.New("native websocket usage decoder unavailable"))
		}
		decodeUsage = rich
	}

	if !recording.dispatch(ctx) {
		return fail("unavailable", 1013, errors.New("usage recording unavailable"))
	}
	exchangeStarted = true
	result.Facts = executeWebSocketExchange(ctx, v2.Exchange{Upstream: n.connection, Initial: initial,
		Decode: decoder,
		Deliver: func(ctx context.Context, frame v2.Frame) (int, error) {
			original := len(frame.Data)
			if storedRound != nil {
				// A provider terminal remains a known result if its observer went
				// away. Persist it with a bounded lifetime before publishing it.
				persisting, stop := context.WithTimeout(context.Background(), 2*time.Second)
				data, err := storedRound.Observe(persisting, frame.Data, n.target)
				stop()
				if err != nil {
					return 0, err
				}
				frame.Data = data
			}
			deliveryStarted = true
			written, err := deliver(ctx, frame)
			if storedRound != nil && err == nil && written == len(frame.Data) {
				return original, nil
			}
			if storedRound != nil && written > 0 {
				return 0, errors.New("stored websocket output delivery incomplete")
			}
			return written, err
		},
	}, decodeUsage, &observed)
	native := observed.native
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

func finishStoredNativeRound(round stored.StoredRound, dispatched bool) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("stored websocket history finalization panicked")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return round.Finish(ctx, dispatched)
}
