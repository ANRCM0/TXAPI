package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

type usageRecording struct {
	id              string
	ctx             context.Context
	failed          bool
	interruptedHost *history.HostFacts
}

// A recording callback cannot replace the upstream outcome by panicking.
func callUsageRecording(ctx context.Context, write func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = history.ErrUnavailable
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return write(ctx)
}

func (g *Gateway) usageQuotationVersion(ctx context.Context) (version string) {
	if g.usageQuotation == nil {
		return ""
	}
	_ = callUsageRecording(ctx, func(ctx context.Context) error {
		current, err := g.usageQuotation.Current(ctx)
		if err == nil && current.Enabled {
			version = current.Version
		}
		return err
	})
	return version
}

func (g *Gateway) beginUsageRecording(c *executionScope, req v1.Request) (func(bool), error) {
	if g.usageHistory == nil || c.source != invocationForeground {
		return func(bool) {}, nil
	}
	g.recordingMu.Lock()
	if g.recordingClosed {
		g.recordingMu.Unlock()
		c.result.RecordingState = "unavailable"
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
		c.result.RecordingState = "unavailable"
		return nil, history.ErrUnavailable
	}
	id := "usage_" + hex.EncodeToString(nonce[:])
	requestID := c.result.Identity.RequestID
	if !routeRequestIDValid(requestID) {
		requestID = ""
	}
	start := history.Start{ID: id, RequestID: requestID, Model: req.Model(), QuotationVersion: g.usageQuotationVersion(c.ctx), Operation: req.Operation(), Stream: req.Stream(), MaxAttempts: g.retryTimes + 1}
	if err := callUsageRecording(c.ctx, func(ctx context.Context) error { return g.usageHistory.Begin(ctx, start) }); err != nil {
		c.result.RecordingState = "unavailable"
		return nil, history.ErrUnavailable
	}
	c.usageRecording = &usageRecording{id: id, ctx: context.WithoutCancel(c.ctx), interruptedHost: &history.HostFacts{Phase: v1.NotDispatched}}
	c.result.UsageRecordID, c.result.RecordingState = id, "open"
	finish := func(returned bool) {
		defer g.recordingActive.Done()
		host := recordingHost(c.result.Result)
		if c.usageRecording.interruptedHost != nil {
			host = *c.usageRecording.interruptedHost
		}
		host.BytesWritten = c.output.written
		// Output ownership belongs to this invocation even when an executor panics.
		interrupted := !returned || c.usageRecording.failed
		func() {
			defer func() {
				if recover() != nil {
					interrupted = true
				}
			}()
			state := c.output.State()
			host.DeliveryCommitted, host.HTTPStatus = state.Committed, state.HTTPStatus
		}()
		completion := history.Completion{ID: id, Host: host, Interrupted: interrupted}
		if err := callUsageRecording(c.usageRecording.ctx, func(ctx context.Context) error { return g.usageHistory.Finish(ctx, completion) }); err != nil {
			c.result.RecordingState = "unavailable"
		} else if interrupted {
			c.result.RecordingState = "interrupted"
		} else {
			c.result.RecordingState = "sealed"
		}
	}
	accepted = true
	handedOff := false
	defer func() {
		if !handedOff {
			finish(false)
		}
	}()
	c.output.setMetadata("X-TXAPI-Usage-ID", id)
	handedOff = true
	return finish, nil
}

func recordingHost(result core.Result) history.HostFacts {
	phase := v1.Phase(result.Phase)
	if phase == "" {
		phase = v1.NotDispatched
	}
	return history.HostFacts{Phase: phase, Status: result.StatusCode, UpstreamStatus: result.UpstreamStatus, HTTPStatus: result.HTTPStatus,
		Termination: v1.Termination(result.Termination), ProtocolComplete: result.ProtocolComplete, DeliveryCommitted: result.Committed, BytesWritten: result.BytesWritten}
}

func usageRecordingFailure() core.Result {
	return core.Result{Phase: core.NotDispatched, StatusCode: 503, Termination: core.UpstreamError,
		Error: &core.Error{StatusCode: 503, Error: core.ErrorDetail{Message: "usage recording is unavailable", Type: "usage_recording_unavailable"}}}
}

func (g *Gateway) prepareUsageRecording(c *executionScope, target history.Target) bool {
	if c.usageRecording == nil {
		return true
	}
	if err := callUsageRecording(c.ctx, func(ctx context.Context) error { return g.usageHistory.Prepare(ctx, target) }); err != nil {
		c.usageRecording.failed = true
		return false
	}
	return true
}

func (g *Gateway) markUsageDispatch(c *executionScope, number int) bool {
	if c.usageRecording == nil {
		return true
	}
	if err := callUsageRecording(c.ctx, func(ctx context.Context) error { return g.usageHistory.MayDispatch(ctx, c.usageRecording.id, number) }); err != nil {
		c.usageRecording.failed = true
		return false
	}
	return true
}

func (g *Gateway) observeUsageRecording(c *executionScope, number int, result core.Result, usage v3.UsageObservation) {
	if c.usageRecording == nil {
		return
	}
	receipt := history.Receipt{ID: c.usageRecording.id, Number: number, Host: recordingHost(result), Usage: cloneDetailedUsageFact(usage)}
	if err := callUsageRecording(c.usageRecording.ctx, func(ctx context.Context) error { return g.usageHistory.Observe(ctx, receipt) }); err != nil {
		c.usageRecording.failed = true
	}
}
