package generation

import (
	"context"
	"errors"

	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
)

// The returned CAS is committed by the task owner together with its terminal
// snapshot and events. A private finalized bit prevents replay from republishing.
func (p *backgroundProgress) preparePublication(ctx context.Context, next *backgroundSnapshot) (*state.HistoryChange, error) {
	if next.HistoryFinalized || !backgroundTerminal(next.NativeState) {
		return nil, nil
	}
	payload, err := p.service.openPayload(p.row)
	if err != nil {
		return nil, err
	}
	if !payload.PublishHistory {
		return nil, nil
	}
	next.HistoryFinalized = true
	if next.NativeState != bg.RemoteSucceeded || !completeBackgroundTarget(next.Target) {
		return nil, nil
	}
	if nilResponseHistoryDependency(p.service.history) || nilResponseHistoryDependency(p.service.projector) {
		return nil, bg.ErrUnavailable
	}
	binding := identity.Binding{UserID: p.row.UserID, TokenID: p.row.TokenID}
	old, content, err := p.service.history.Read(ctx, binding, p.row.PublicID)
	if errors.Is(err, state.ErrHistoryNotFound) || errors.Is(err, access.ErrUnauthorized) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if old.Kind != "response" || old.Model != p.row.Model || old.Version != 1 || old.Busy != bs.HistoryBusy(p.row.ID) || content.Ready || content.Active != old.Busy {
		return nil, nil
	}
	projected, err := p.service.projector.ProjectBackgroundHistory(payload.Body, next.Document, p.row.PublicID, payload.ParentID, p.row.Model)
	if err != nil {
		return nil, nil
	}
	row := p.service.history.Fresh(old.ID, old.Kind, old.Model, binding)
	row.Version = old.Version + 1
	row, err = p.service.history.Seal(row, history.Content{Request: projected.Request, Input: projected.Input, Items: projected.Items, Response: projected.Response, Parent: payload.ParentID, Ready: true, Target: next.Target})
	if errors.Is(err, state.ErrHistoryCapacity) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state.HistoryChange{Record: row, Expected: old.Version}, nil
}
