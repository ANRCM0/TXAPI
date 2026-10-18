package generation

import (
	"bytes"
	"context"
	"maps"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	lifecycle "minigateway/internal/foundation/contracts/lifecycle/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

type publicArchive struct{ archive *Archive }

// PublicArchive exposes owned snapshots and per-generation resources. It never
// exposes the shared store, key ring, runner or internal record bookkeeping.
func PublicArchive(archive *Archive) state.Archive {
	if archive == nil {
		return nil
	}
	return &publicArchive{archive: archive}
}

func (p *publicArchive) BeginForeground(ctx context.Context, binding identity.Binding, model string, target execv1.Output) (state.Foreground, error) {
	foreground, err := p.archive.BeginForeground(ctx, binding, model, target)
	if err != nil {
		return nil, err
	}
	return foreground, nil
}

func (p *publicArchive) Read(ctx context.Context, binding identity.Binding, id string) (state.Recording, error) {
	in, err := p.archive.Read(ctx, binding, id)
	return state.Recording{Metadata: execv1.ResponseMetadata{StatusCode: in.Metadata.StatusCode, ContentType: in.Metadata.ContentType, Values: maps.Clone(in.Metadata.Values)}, Body: bytes.Clone(in.Body), Result: in.Result}, err
}

func (p *publicArchive) Delete(ctx context.Context, binding identity.Binding, id string) error {
	return p.archive.Delete(ctx, binding, id)
}

func (p *publicArchive) OpenCursor(ctx context.Context, binding identity.Binding, id, token string) (state.Cursor, error) {
	cursor, err := p.archive.OpenCursor(ctx, binding, id, token)
	if err != nil {
		return nil, err
	}
	return cursor, nil
}

var _ lifecycle.Drainer = (*Runner)(nil)
var _ lifecycle.Drainer = (*DurableRunner)(nil)
var _ state.Foreground = (*Foreground)(nil)
var _ state.Cursor = (*CursorReader)(nil)
