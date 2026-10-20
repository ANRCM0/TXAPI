package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	execv3 "minigateway/internal/foundation/contracts/execution/v3"
	lifecycle "minigateway/internal/foundation/contracts/lifecycle/v1"
	bs "minigateway/internal/generation/backgroundstate"
)

type backgroundSnapshot struct {
	Version          int `json:"private_version"`
	Target           execv1.TargetConstraint
	RemoteID         string
	Document         json.RawMessage
	Usage            *execv1.Usage
	Rate             bs.RateReservation
	Failures         int
	NativeState      bg.RemoteState           `json:"native_state,omitempty"`
	EventComplete    bool                     `json:"event_complete,omitempty"`
	EventUnavailable bool                     `json:"event_unavailable,omitempty"`
	RateFinalized    bool                     `json:"rate_finalized,omitempty"`
	DetailedUsage    *execv3.UsageObservation `json:"detailed_usage,omitempty"`
}

func (s *BackgroundService) openSnapshot(r bs.Record) (backgroundSnapshot, error) {
	if len(r.Snapshot) == 0 {
		return backgroundSnapshot{}, nil
	}
	plain, e := s.keys.Open(r.Snapshot, backgroundAAD(r, "snapshot"))
	var out backgroundSnapshot
	if e != nil || json.Unmarshal(plain, &out) != nil || out.Version != 1 || len(out.Document) > 1<<20 || out.Target.ChannelID == 0 || out.Target.CredentialID == 0 || out.Target.Fingerprint == "" {
		return backgroundSnapshot{}, bg.ErrUnavailable
	}
	if (r.Stage == string(bg.StageRemoteActive) || r.ReplayPending) && out.RemoteID == "" {
		return backgroundSnapshot{}, bg.ErrUnavailable
	}
	// Legacy dispatch snapshots encode an absent RawMessage as JSON null.
	if bytes.Equal(bytes.TrimSpace(out.Document), []byte("null")) {
		out.Document = nil
	}
	if out.DetailedUsage != nil && out.DetailedUsage.Validate() != nil {
		return backgroundSnapshot{}, bg.ErrUnavailable
	}
	return out, nil
}
func (s *BackgroundService) sealSnapshot(r bs.Record, in backgroundSnapshot) ([]byte, error) {
	in.Version = 1
	plain, e := json.Marshal(in)
	if e != nil || len(plain) > bs.MaxContent {
		return nil, bg.ErrCapacity
	}
	return s.keys.Seal(plain, backgroundAAD(r, "snapshot"))
}

type BackgroundRunnerOptions struct {
	Service *BackgroundService
	Remote  bs.Remote
	Resolve func(context.Context, access.Binding) (access.Principal, error)
}
type BackgroundRunner struct {
	opts           BackgroundRunnerOptions
	cancel         context.CancelFunc
	done           chan struct{}
	activeControls atomic.Int64
}

// ActiveControls counts claimed control operations through release cleanup.
// It is not the number of remote generations or currently valid leases.
func (r *BackgroundRunner) ActiveControls() int64 {
	if r == nil {
		return 0
	}
	return r.activeControls.Load()
}

// Fixed, queue-less workers bound control traffic separately from generation
// admission. Client observation contexts are never retained by this owner.
func NewBackgroundRunner(parent context.Context, o BackgroundRunnerOptions) (*BackgroundRunner, error) {
	if parent == nil || o.Service == nil || nilPort(o.Remote) || o.Resolve == nil {
		return nil, bg.ErrUnavailable
	}
	if e := parent.Err(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(parent, cancel)
	r := &BackgroundRunner{opts: o, cancel: cancel, done: make(chan struct{})}
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					if e := r.runOne(ctx); e != nil && !errors.Is(e, bg.ErrNotFound) && ctx.Err() == nil {
						slog.Warn("background task control deferred; inspect retained task state")
					}
					timer.Reset(time.Second)
				}
			}
		}()
	}
	go func() { group.Wait(); stop(); cancel(); close(r.done) }()
	return r, nil
}
func (r *BackgroundRunner) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *BackgroundRunner) runOne(parent context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = bg.ErrUnavailable
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	s := r.opts.Service
	row, claim, e := s.repository.ClaimBackground(ctx, 30*time.Second)
	if e != nil {
		return e
	}
	r.activeControls.Add(1)
	defer r.activeControls.Add(-1)
	p := &backgroundProgress{service: s, remote: r.opts.Remote, row: row, claim: claim}
	defer func() {
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		due := max(time.Now().Add(time.Second).UnixMilli(), p.row.DueAt)
		_ = s.repository.ReleaseBackground(cleanup, claim, due)
	}()
	p.snapshot, e = s.openSnapshot(row)
	if e != nil {
		return e
	}
	call := bs.RemoteCall{Operation: execv1.Operation(row.Operation), Model: row.Model, PublicID: row.PublicID, Stream: row.Stream}
	if row.Stage == string(bg.StageQueued) {
		if row.CancelRequested || row.Deadline <= time.Now().UnixMilli() {
			return p.rejectQueued(ctx)
		}
		principal, e := currentPrincipal(ctx, r.opts.Resolve, access.Binding{UserID: row.UserID, TokenID: row.TokenID})
		if errors.Is(e, access.ErrUnauthorized) || e == nil && !principal.Policy.Allows(row.Model) {
			return p.rejectQueued(ctx)
		}
		if e != nil {
			return e
		}
		ctx = access.WithPrincipal(access.NewGenerationContext(ctx), principal)
		payload, e := s.openPayload(row)
		if e != nil || payload.Operation != call.Operation {
			return bg.ErrUnavailable
		}
		call.Action = bg.RemoteSubmit
		call.Body = bytes.Clone(payload.Body)
		call.Target = payload.Target
	} else if row.Stage == string(bg.StageRemoteActive) || row.ReplayPending {
		call.Action = bg.RemoteInspect
		if row.Stream && !p.snapshot.EventUnavailable {
			call.Action = bg.RemoteResume
		}
		// Revocation blocks content and new work, not cleanup of already-bound
		// work. Cancellation uses its fenced claim, never a synthetic principal.
		principal, e := currentPrincipal(ctx, r.opts.Resolve, access.Binding{UserID: row.UserID, TokenID: row.TokenID})
		if !row.ReplayPending && (row.CancelRequested || row.Deadline <= time.Now().UnixMilli() || errors.Is(e, access.ErrUnauthorized) || e == nil && !principal.Policy.Allows(row.Model)) {
			call.Action = bg.RemoteCancel
		}
		if e != nil && !errors.Is(e, access.ErrUnauthorized) {
			return e
		}
		if call.Action == bg.RemoteCancel && !row.CancelRequested {
			p.row, e = s.repository.RequestBackgroundCancel(ctx, claim)
			if e != nil {
				return e
			}
		}
		target := p.snapshot.Target
		call.Target = &target
		call.RemoteID = p.snapshot.RemoteID
		if call.Action == bg.RemoteResume && row.LastSequence >= 0 {
			n := uint64(row.LastSequence)
			call.After = &n
		}
	} else {
		return bg.ErrConflict
	}
	e = r.opts.Remote.Execute(ctx, call, p)
	if row.Stream && p.snapshot.RemoteID != "" && (errors.Is(e, bg.ErrCursorUnavailable) || errors.Is(e, bg.ErrCapacity) || errors.Is(e, bg.ErrUnsupportedCapability)) {
		if end := p.markEventsUnavailable(ctx); end != nil {
			return end
		}
		return e
	}
	if call.Action == bg.RemoteSubmit && (errors.Is(e, bg.ErrCapacity) || errors.Is(e, bg.ErrUnsupportedCapability) || errors.Is(e, bg.ErrUnauthorized)) {
		if end := p.rejectQueued(ctx); end != nil {
			return end
		}
	}
	if e != nil && call.Action != bg.RemoteSubmit {
		_ = p.backoff(ctx)
	}
	return e
}

type backgroundProgress struct {
	service  *BackgroundService
	remote   bs.Remote
	row      bs.Record
	claim    bs.Claim
	snapshot backgroundSnapshot
}

func (p *backgroundProgress) refresh(ctx context.Context) error {
	row, e := p.service.repository.ReadClaimedBackground(ctx, p.claim)
	if e != nil {
		return e
	}
	if row.ID != p.row.ID || row.PublicID != p.row.PublicID || row.Deleted || row.ContentExpiresAt <= time.Now().UnixMilli() {
		return bs.ErrClaimLost
	}
	p.row = row
	return nil
}
func (p *backgroundProgress) Validate(ctx context.Context, call bs.RemoteCall) error {
	if e := p.refresh(ctx); e != nil {
		return e
	}
	if call.Operation != execv1.Operation(p.row.Operation) || call.Model != p.row.Model || call.PublicID != p.row.PublicID {
		return bg.ErrConflict
	}
	if call.Action == bg.RemoteSubmit {
		if p.row.Stage != string(bg.StageQueued) || call.RemoteID != "" || call.After != nil || call.Stream != p.row.Stream {
			return bg.ErrConflict
		}
		payload, e := p.service.openPayload(p.row)
		if e != nil || !bytes.Equal(payload.Body, call.Body) || (call.Target == nil) != (payload.Target == nil) || call.Target != nil && *call.Target != *payload.Target {
			return bg.ErrConflict
		}
		return nil
	}
	if (p.row.Stage != string(bg.StageRemoteActive) && !p.row.ReplayPending) || call.Target == nil || *call.Target != p.snapshot.Target || call.RemoteID != p.snapshot.RemoteID || len(call.Body) != 0 || call.Stream != p.row.Stream || call.Action != bg.RemoteInspect && call.Action != bg.RemoteCancel && call.Action != bg.RemoteResume {
		return bg.ErrConflict
	}
	if p.row.ReplayPending && call.Action != bg.RemoteResume {
		return bg.ErrConflict
	}
	if call.Action == bg.RemoteResume {
		if !p.row.Stream || p.snapshot.EventUnavailable || (call.After == nil) != (p.row.LastSequence < 0) || call.After != nil && *call.After != uint64(p.row.LastSequence) {
			return bg.ErrConflict
		}
	} else if call.After != nil {
		return bg.ErrConflict
	}
	return nil
}
func (p *backgroundProgress) rejectQueued(ctx context.Context) error {
	if e := p.refresh(ctx); e != nil {
		return e
	}
	if p.row.Stage != string(bg.StageQueued) {
		return bg.ErrConflict
	}
	_, e := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.StageTerminal})
	return e
}
func (p *backgroundProgress) Dispatch(ctx context.Context, target execv1.TargetConstraint, rate bs.RateReservation) error {
	if e := p.refresh(ctx); e != nil {
		return e
	}
	if p.row.Stage != string(bg.StageQueued) || target.ChannelID == 0 || target.CredentialID == 0 || target.Fingerprint == "" {
		return bg.ErrConflict
	}
	payload, e := p.service.openPayload(p.row)
	if e != nil || payload.Target != nil && *payload.Target != target {
		return bg.ErrConflict
	}
	p.snapshot = backgroundSnapshot{Target: target, Rate: rate}
	snapshot, e := p.service.sealSnapshot(p.row, p.snapshot)
	if e != nil {
		return e
	}
	row, e := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.StageDispatching, Snapshot: snapshot})
	if e == nil {
		p.row = row
	}
	return e
}
func (p *backgroundProgress) backoff(ctx context.Context) error {
	if e := p.refresh(ctx); e != nil {
		return e
	}
	if p.row.Stage != string(bg.StageRemoteActive) && !p.row.ReplayPending {
		return bg.ErrConflict
	}
	next := p.snapshot
	next.Failures = min(5, max(0, next.Failures)+1)
	snapshot, e := p.service.sealSnapshot(p.row, next)
	if e != nil {
		return e
	}
	due := time.Now().Add(time.Duration(1<<next.Failures) * time.Second).UnixMilli()
	row, e := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.Stage(p.row.Stage), ReplayPending: p.row.ReplayPending, Snapshot: snapshot, DueAt: due})
	if e == nil {
		p.row = row
		p.snapshot = next
	}
	return e
}

var _ lifecycle.Drainer = (*BackgroundRunner)(nil)
