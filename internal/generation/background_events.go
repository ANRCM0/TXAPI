package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	bs "minigateway/internal/generation/backgroundstate"
)

func backgroundTerminal(s bg.RemoteState) bool {
	return s == bg.RemoteSucceeded || s == bg.RemoteIncomplete || s == bg.RemoteFailed || s == bg.RemoteCancelled
}

func (p *backgroundProgress) prepareEvents(ctx context.Context, events []bg.Event) ([]bs.Event, error) {
	if len(events) > 128 {
		return nil, bg.ErrCapacity
	}
	out := make([]bs.Event, 0, len(events))
	for _, event := range events {
		if !p.row.Stream || event.Sequence > math.MaxInt64 || len(event.Data) == 0 || len(event.Data) > 64<<10 {
			return nil, bg.ErrCursorUnavailable
		}
		seq := int64(event.Sequence)
		aad := backgroundAAD(p.row, "event/"+strconv.FormatInt(seq, 10))
		old, found, e := p.service.repository.ReadClaimedBackgroundEvent(ctx, p.claim, seq)
		if e != nil {
			return nil, e
		}
		if !found && seq <= p.row.LastSequence {
			return nil, bg.ErrCursorUnavailable
		}
		var cipher []byte
		if found {
			plain, e := p.service.keys.Open(old.Ciphertext, aad)
			if e != nil || !bytes.Equal(plain, event.Data) {
				return nil, bg.ErrCursorUnavailable
			}
			cipher = bytes.Clone(old.Ciphertext)
		} else {
			cipher, e = p.service.keys.Seal(event.Data, aad)
			if e != nil {
				return nil, bg.ErrUnavailable
			}
		}
		out = append(out, bs.Event{TaskID: p.row.ID, Sequence: seq, Ciphertext: cipher})
	}
	return out, nil
}

func (p *backgroundProgress) Observe(ctx context.Context, o bg.Observation) error {
	if o.RemoteID == "" || len(o.Document) > 1<<20 || len(o.Document) == 0 && len(o.Events) == 0 || len(o.Document) > 0 && !json.Valid(o.Document) {
		return bg.ErrCursorUnavailable
	}
	if p.snapshot.RemoteID != "" && p.snapshot.RemoteID != o.RemoteID {
		return bg.ErrCursorUnavailable
	}
	if o.State != "" && o.State != bg.RemoteAccepted && o.State != bg.RemoteRunning && !backgroundTerminal(o.State) || o.State == "" && len(o.Events) == 0 {
		return bg.ErrCursorUnavailable
	}
	next := p.snapshot
	next.RemoteID, next.Failures = o.RemoteID, 0
	wasTerminal, nowTerminal := backgroundTerminal(next.NativeState), backgroundTerminal(o.State)
	if wasTerminal && nowTerminal && next.NativeState != o.State {
		return bg.ErrCursorUnavailable
	}
	// A resumed historical prefix cannot regress a known terminal response.
	if !wasTerminal || nowTerminal {
		if len(o.Document) > 0 {
			next.Document = bytes.Clone(o.Document)
		}
		if o.State != "" {
			next.NativeState = o.State
		}
	}
	if nowTerminal {
		if wasTerminal && next.Usage != nil && o.Usage != nil && *next.Usage != *o.Usage {
			return bg.ErrCursorUnavailable
		}
		if !wasTerminal {
			next.Usage = nil
		}
		if o.Usage != nil {
			u := *o.Usage
			next.Usage = &u
		}
		if len(o.Events) > 0 {
			next.EventComplete = true
		}
	}
	stage := bg.StageRemoteActive
	if backgroundTerminal(next.NativeState) {
		stage = bg.StageTerminal
	}
	finishRate := stage == bg.StageTerminal && !next.RateFinalized && next.Usage != nil
	if finishRate {
		next.RateFinalized = true
	}
	for range 3 {
		if e := p.refresh(ctx); e != nil {
			return e
		}
		events, e := p.prepareEvents(ctx, o.Events)
		if e != nil {
			return e
		}
		snapshot, e := p.service.sealSnapshot(p.row, next)
		if e != nil {
			return e
		}
		pending := stage == bg.StageTerminal && p.row.Stream && !next.EventComplete && !next.EventUnavailable
		row, e := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: stage, Snapshot: snapshot, Events: events, ReplayPending: pending, DueAt: time.Now().Add(time.Second).UnixMilli()})
		if errors.Is(e, bg.ErrConflict) {
			continue
		}
		if e != nil {
			return e
		}
		p.row, p.snapshot = row, next
		if finishRate {
			p.remote.FinishRate(ctx, row.TokenID, next.Rate, next.Usage)
		}
		return nil
	}
	return bg.ErrConflict
}

func (p *backgroundProgress) markEventsUnavailable(ctx context.Context) error {
	for range 3 {
		if e := p.refresh(ctx); e != nil {
			return e
		}
		next := p.snapshot
		next.EventUnavailable = true
		snapshot, e := p.service.sealSnapshot(p.row, next)
		if e != nil {
			return e
		}
		row, e := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.Stage(p.row.Stage), Snapshot: snapshot, DueAt: time.Now().Add(time.Second).UnixMilli()})
		if errors.Is(e, bg.ErrConflict) {
			continue
		}
		if e != nil {
			return e
		}
		p.row, p.snapshot = row, next
		return nil
	}
	return bg.ErrConflict
}
