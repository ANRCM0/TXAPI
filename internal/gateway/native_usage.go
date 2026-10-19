package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"

	"minigateway/internal/access"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

type nativeUsageRecording struct {
	gateway          *Gateway
	id               string
	ctx              context.Context
	prepared, failed bool
}

func (g *Gateway) beginNativeUsage(ctx context.Context, model string) (*nativeUsageRecording, error) {
	if g.usageHistory == nil {
		return nil, nil
	}
	g.recordingMu.Lock()
	if g.recordingClosed {
		g.recordingMu.Unlock()
		return nil, history.ErrUnavailable
	}
	g.recordingActive.Add(1)
	g.recordingMu.Unlock()
	accepted := false
	defer func() {
		if !accepted {
			g.recordingActive.Done()
		}
	}()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, history.ErrUnavailable
	}
	id := "usage_" + hex.EncodeToString(nonce[:])
	requestID := access.RequestID(ctx)
	if !routeRequestIDValid(requestID) {
		requestID = ""
	}
	start := history.Start{ID: id, RequestID: requestID, Transport: "websocket", Model: model, Operation: v1.Responses, Stream: true, MaxAttempts: 1, QuotationVersion: g.usageQuotationVersion(ctx)}
	if err := callUsageRecording(ctx, func(ctx context.Context) error { return g.usageHistory.Begin(ctx, start) }); err != nil {
		return nil, history.ErrUnavailable
	}
	accepted = true
	return &nativeUsageRecording{gateway: g, id: id, ctx: context.WithoutCancel(ctx)}, nil
}

func (r *nativeUsageRecording) prepare(ctx context.Context, target history.Target) bool {
	if r == nil {
		return true
	}
	target.ID, target.Number = r.id, 1
	if err := callUsageRecording(ctx, func(ctx context.Context) error { return r.gateway.usageHistory.Prepare(ctx, target) }); err != nil {
		r.failed = true
		return false
	}
	r.prepared = true
	return true
}

func (r *nativeUsageRecording) dispatch(ctx context.Context) bool {
	if r == nil {
		return true
	}
	if err := callUsageRecording(ctx, func(ctx context.Context) error { return r.gateway.usageHistory.MayDispatch(ctx, r.id, 1) }); err != nil {
		r.failed = true
		return false
	}
	return true
}

func nativeUsageHost(f v2.ExchangeResult, ctx context.Context) history.HostFacts {
	host := history.HostFacts{Phase: f.Dispatch, Termination: f.Termination, ProtocolComplete: f.ProtocolComplete, DeliveryCommitted: f.DeliveryCommitted}
	if host.Phase == "" {
		host.Phase = v1.NotDispatched
	}
	switch {
	case errors.Is(f.Err, context.DeadlineExceeded), f.Termination == v2.Canceled && errors.Is(ctx.Err(), context.DeadlineExceeded):
		host.Termination = v1.Deadline
	case host.Termination == v2.Failed:
		host.Termination = v1.UpstreamError
	}
	// Each payload is bounded, and the round is time-limited. Preserve the lower
	// bound defensively if a custom connection supplies an overflowing counter.
	host.BytesWritten = int64(min(f.DeliveryBytesWritten, uint64(math.MaxInt64)))
	return host
}

func (r *nativeUsageRecording) finish(ctx context.Context, result *contract.Result, usage v3.UsageObservation, status string, interrupted bool) {
	if r == nil {
		return
	}
	defer r.gateway.recordingActive.Done()
	host := nativeUsageHost(result.Facts, ctx)
	host.ProtocolStatus = status
	if r.prepared {
		receipt := history.Receipt{ID: r.id, Number: 1, Host: host, Usage: cloneDetailedUsageFact(usage)}
		if err := callUsageRecording(r.ctx, func(ctx context.Context) error { return r.gateway.usageHistory.Observe(ctx, receipt) }); err != nil {
			r.failed = true
		}
	}
	completion := history.Completion{ID: r.id, Host: host, Interrupted: interrupted || r.failed}
	if err := callUsageRecording(r.ctx, func(ctx context.Context) error { return r.gateway.usageHistory.Finish(ctx, completion) }); err != nil {
		result.RecordingState = "unavailable"
	} else if completion.Interrupted {
		result.RecordingState = "interrupted"
	} else {
		result.RecordingState = "sealed"
	}
}
