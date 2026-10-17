package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
)

// IntentLeases owns atomic claim/renewal and financial-safe rejection. It is
// neither a database callback nor permission to create an uncosted attempt.
type IntentLeases interface {
	ClaimGenerationIntent(context.Context, time.Duration) (model.GenerationIntent, model.GenerationClaim, error)
	RenewGenerationClaim(context.Context, model.GenerationClaim, time.Duration) (bool, error)
	AttachGenerationOutput(context.Context, model.GenerationClaim, string) error
	CompleteGenerationOutput(context.Context, model.GenerationClaim) error
	RejectGenerationClaim(context.Context, model.GenerationClaim) error
	QuarantineGenerationClaim(context.Context, model.GenerationClaim) error
}

type DurableOptions struct {
	Acceptor      *IntentAcceptor
	Leases        IntentLeases
	Archive       *Archive
	Workers       int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	Execute       func(context.Context, core.Output, gateway.Execution, model.GenerationClaim) gateway.RequestResult
}

func nilPort(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// DurableRunner polls only when a bounded worker is available; there is no
// second in-memory queue. Observation and submission contexts are not retained.
type DurableRunner struct {
	opts   DurableOptions
	cancel context.CancelFunc
	done   chan struct{}
}

func NewDurableRunner(parent context.Context, opts DurableOptions) (*DurableRunner, error) {
	if parent == nil || opts.Acceptor == nil || opts.Archive == nil || opts.Execute == nil || nilPort(opts.Leases) ||
		opts.Workers < 1 || opts.Workers > 32 || opts.PollInterval < 20*time.Millisecond || opts.PollInterval > time.Minute ||
		opts.LeaseDuration < time.Second || opts.LeaseDuration > time.Minute || opts.Archive.retention < opts.Acceptor.queueTTL+5*time.Second {
		return nil, fmt.Errorf("invalid durable generation configuration")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(parent, cancel)
	r := &DurableRunner{opts: opts, cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					if err := r.runOne(ctx); err != nil && !errors.Is(err, model.ErrIntentNotFound) && ctx.Err() == nil {
						// Never log opaque storage/callback errors containing content.
						slog.Warn("durable generation did not complete; inspect intent and ledger state")
					}
					timer.Reset(opts.PollInterval)
				}
			}
		}()
	}
	go func() { workers.Wait(); stop(); cancel(); close(r.done) }()
	return r, nil
}

func (r *DurableRunner) runOne(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrExecutionPanic
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	in, claim, err := r.opts.Leases.ClaimGenerationIntent(claimCtx, r.opts.LeaseDuration)
	cancel()
	if err != nil {
		return err
	}
	return r.consume(ctx, in, claim)
}

func (r *DurableRunner) consume(parent context.Context, in model.GenerationIntent, claim model.GenerationClaim) (err error) {
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(in.Deadline))
	defer cancel()
	stop, joined := make(chan struct{}), make(chan struct{})
	// The heartbeat keeps the initial lease context, not the execution context
	// variable that is enriched with the freshly resolved Principal below.
	go func(ctx context.Context) {
		defer func() {
			if recover() != nil {
				cancel()
			}
			close(joined)
		}()
		ticker := time.NewTicker(r.opts.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(ctx, min(r.opts.LeaseDuration/3, 2*time.Second))
				requested, renewErr := r.opts.Leases.RenewGenerationClaim(renewCtx, claim, r.opts.LeaseDuration)
				renewCancel()
				if renewErr != nil || requested {
					cancel()
					return
				}
			}
		}
	}(ctx)
	defer func() { close(stop); <-joined }()
	var output *recordOutput
	quarantine := false
	result := gateway.RequestResult{Result: core.Result{Phase: core.NotDispatched, StatusCode: 503}}
	defer func() {
		if recover() != nil {
			err = ErrExecutionPanic
			result.Replayable = false
		}
		if output != nil {
			if finishErr := output.finish(result, err); finishErr != nil {
				err = errors.Join(err, finishErr)
			}
		}
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer finalCancel()
		if quarantine {
			err = errors.Join(err, r.opts.Leases.QuarantineGenerationClaim(finalCtx, claim))
			return
		}
		if err == nil && output != nil {
			err = r.opts.Leases.CompleteGenerationOutput(finalCtx, claim)
			if err != nil {
				return
			}
		}
		// Also handles all failures preceding reservation adoption. If output or
		// execution became uncertain, this cannot refund an attempted intent.
		err = errors.Join(err, r.opts.Leases.RejectGenerationClaim(finalCtx, claim))
	}()
	if in.ID != claim.ID || in.ClaimEpoch != claim.Epoch || in.State != model.IntentClaimed {
		return ErrIntentIntegrity
	}
	execution, err := r.opts.Acceptor.openIntent(in)
	if err != nil {
		quarantine = true
		return err
	}
	binding := access.Binding{UserID: in.UserID, TokenID: in.TokenID}
	principal, err := currentPrincipal(ctx, r.opts.Acceptor.resolve, binding)
	if err != nil {
		return err
	}
	if !principal.Policy.Allows(in.Model) {
		return access.ErrUnauthorized
	}
	output, err = r.opts.Archive.start(ctx, binding)
	if err != nil {
		return err
	}
	output.model = in.Model
	if err = r.opts.Leases.AttachGenerationOutput(ctx, claim, output.record.ID); err != nil {
		return err
	}
	// Refresh after storage preparation; no queued permission/limit snapshot
	// becomes authority for a later generation or survives a revoked binding.
	principal, err = currentPrincipal(ctx, r.opts.Acceptor.resolve, binding)
	if err != nil {
		return err
	}
	if !principal.Policy.Allows(in.Model) {
		return access.ErrUnauthorized
	}
	ctx = access.WithPrincipal(ctx, principal)
	ctx = access.WithRequestID(ctx, in.ID)
	result.Phase = core.Dispatching
	result = r.opts.Execute(ctx, output, execution, claim)
	return nil
}

// Close cancels claim polling and joins network execution, lease renewal,
// output persistence and financial finalization before dependencies may close.
func (r *DurableRunner) Close(ctx context.Context) error {
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
