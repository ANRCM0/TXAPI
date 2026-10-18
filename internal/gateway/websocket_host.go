package gateway

import (
	"context"
	"errors"
	"time"

	admission "minigateway/internal/foundation/contracts/admission/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

type websocketCreate struct {
	model, previous string
	initial         v2.Frame
}
type websocketDialResource interface {
	websocketResource
	Dial(context.Context) (v2.Connection, error)
}

// This controlled single-round consumer has no product route or continuation
// cache. Session/target selection is a separate product batch.
type websocketHost struct {
	authority       identity.Authority
	gate            admission.RequestGate
	admission       Admission
	resources       *websocketResources
	parse           func(v2.Frame) (websocketCreate, error)
	decoder         func(string) v2.FrameDecoder
	billingDisabled func() bool
	allowed         func(string) bool
}

func (h *websocketHost) execute(ctx context.Context, binding identity.Binding, frame v2.Frame, deliver v2.FrameDelivery) v2.ExchangeResult {
	fail := func(err error) v2.ExchangeResult {
		return v2.ExchangeResult{Dispatch: v2.NotDispatched, Termination: v2.Failed, Usage: unknownUsageFact(), Err: err}
	}
	if ctx == nil || h == nil || nilInterface(h.authority) || nilInterface(h.gate) || nilInterface(h.admission) || h.resources == nil || h.parse == nil || h.decoder == nil || h.billingDisabled == nil || h.allowed == nil || deliver == nil {
		return fail(errors.New("native websocket host unavailable"))
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	refreshed, principal, err := h.authority.Refresh(ctx, binding)
	if err != nil {
		return fail(err)
	}
	ctx = refreshed
	create, err := h.parse(frame)
	if err != nil {
		return fail(err)
	}
	if !principal.Policy.Allows(create.model) || !h.allowed(create.model) || !h.billingDisabled() || create.previous != "" {
		return fail(errors.New("native websocket request is not allowed"))
	}
	// TPM bounds are not yet proven for WS. Reject rather than bypassing an
	// enabled token limit with an untrustworthy zero estimate.
	if principal.TPMLimit != 0 {
		return fail(errors.New("native websocket TPM policy unsupported"))
	}
	ctx, decision := h.gate.Admit(ctx, func() int { return 0 })
	if decision.Rejected {
		return fail(errors.New("native websocket rate limit"))
	}
	defer h.gate.Finish(ctx, 0)
	scope := AdmissionScope{Level: RequestAdmission, UserID: binding.UserID, TokenID: binding.TokenID, Model: create.model}
	release, err := h.admission.Acquire(ctx, scope)
	if release != nil {
		defer release()
	}
	if err != nil || release == nil {
		return fail(errors.New("native websocket capacity unavailable"))
	}
	lease, err := h.resources.acquire()
	if err != nil {
		return fail(err)
	}
	defer lease.Release()
	// The private lease pins the complete immutable resource snapshot. No
	// mutable registry lookup occurs after acquiring a different revision.
	owned := lease.(*websocketResourceLease).generation.resource
	resource, ok := owned.(websocketDialResource)
	if !ok || nilInterface(resource) {
		return fail(errors.New("native websocket resources unavailable"))
	}
	connection, err := resource.Dial(ctx)
	if err != nil {
		return fail(err)
	}
	if nilInterface(connection) {
		return fail(errors.New("native websocket dial returned no connection"))
	}
	defer connection.Close()
	return (WebSocketExchangeExecutor{}).ExecuteExchange(ctx, v2.Exchange{Upstream: connection, Initial: create.initial, Decode: h.decoder(create.model), Deliver: deliver})
}
