package generation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

type storedResponseRounds struct {
	repository history.Repository
	codec      history.StoredCodec
}

func NewStoredResponseRounds(repository history.Repository, codec history.StoredCodec) (history.StoredRounds, error) {
	if nilResponseHistoryDependency(repository) || nilResponseHistoryDependency(codec) {
		return nil, errors.New("stored Responses history dependencies are required")
	}
	return &storedResponseRounds{repository: repository, codec: codec}, nil
}

func (s *storedResponseRounds) Prepare(ctx context.Context, binding identity.Binding, create history.StoredCreate) (history.StoredRound, error) {
	if ctx == nil || binding.UserID == 0 || binding.TokenID == 0 || create.Model == "" || len(create.Model) > 200 || len(create.JSON) > history.MaxContextBytes {
		return nil, state.ErrHistoryContext
	}
	var inherited []json.RawMessage
	var guards []state.HistoryGuard
	var target *execv1.TargetConstraint
	if create.PreviousID != "" {
		if !strings.HasPrefix(create.PreviousID, "resp_") || len(create.PreviousID) > 80 {
			return nil, state.ErrHistoryNotFound
		}
		parent, content, err := s.repository.Read(ctx, binding, create.PreviousID)
		if err != nil {
			return nil, err
		}
		if parent.Kind != "response" || parent.Model != create.Model || !content.Ready || parent.Busy != "" || content.Active != "" {
			return nil, state.ErrHistoryConflict
		}
		if len(content.Items) > history.MaxContextItems || content.Target.Fingerprint == "" || content.Target.ChannelID == 0 || content.Target.CredentialID == 0 || content.Target.ActualModel == "" {
			return nil, state.ErrHistoryContext
		}
		inherited = content.Items
		copy := content.Target
		target = &copy
		guards = []state.HistoryGuard{{ID: parent.ID, Version: parent.Version}}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := "resp_" + hex.EncodeToString(nonce[:])
	compiled, err := s.codec.Compile(bytes.Clone(create.JSON), inherited, id)
	if err != nil {
		return nil, state.ErrHistoryContext
	}
	if len(compiled.Input) > history.MaxContextItems || compiled.Input == nil || len(compiled.JSON) == 0 || len(compiled.JSON) > history.MaxContextBytes {
		return nil, state.ErrHistoryCapacity
	}
	row := s.repository.Fresh(id, "response", create.Model, binding)
	row.Busy = "ws_" + hex.EncodeToString(nonce[:])
	content := history.Content{Request: bytes.Clone(compiled.JSON), Active: row.Busy, Input: cloneHistoryItems(compiled.Input), Parent: create.PreviousID}
	row, err = s.repository.Seal(row, content)
	if err != nil {
		return nil, err
	}
	if err = s.repository.Apply(ctx, binding, []state.HistoryChange{{Record: row}}, guards...); err != nil {
		return nil, err
	}
	return &storedResponseRound{service: s, binding: binding, row: row, content: content,
		prepared: history.PreparedRound{ID: id, JSON: bytes.Clone(compiled.JSON), PromptText: compiled.PromptText, Target: target}}, nil
}

type storedResponseRound struct {
	service  *storedResponseRounds
	binding  identity.Binding
	row      state.ResponseHistory
	content  history.Content
	prepared history.PreparedRound
	finished bool
	terminal bool
}

func (r *storedResponseRound) Prepared() history.PreparedRound {
	out := r.prepared
	out.JSON = bytes.Clone(out.JSON)
	if out.Target != nil {
		copy := *out.Target
		out.Target = &copy
	}
	return out
}

func (r *storedResponseRound) Observe(ctx context.Context, raw json.RawMessage, target execv1.TargetConstraint) (json.RawMessage, error) {
	if r.finished || r.terminal || ctx == nil || len(raw) > history.MaxContextBytes {
		return nil, state.ErrHistoryConflict
	}
	event, err := r.service.codec.Project(raw, r.row.ID, r.content.Parent, r.row.Model)
	if err != nil {
		return nil, err
	}
	if len(event.JSON) == 0 || len(event.JSON) > history.MaxContextBytes {
		return nil, state.ErrHistoryCapacity
	}
	if !event.Terminal {
		return event.JSON, nil
	}
	if len(event.Response) == 0 || event.Output == nil || len(r.content.Input)+len(event.Output) > history.MaxContextItems || target.Fingerprint == "" || target.ChannelID == 0 || target.CredentialID == 0 || target.ActualModel == "" || r.prepared.Target != nil && *r.prepared.Target != target {
		return nil, state.ErrHistoryContext
	}
	content := r.content
	content.Ready, content.Active = true, ""
	content.Items = append(cloneHistoryItems(content.Input), cloneHistoryItems(event.Output)...)
	content.Response, content.Target = bytes.Clone(event.Response), target
	row := r.row
	row.Version++
	row.Busy = ""
	row, err = r.service.repository.Seal(row, content)
	if err != nil {
		return nil, err
	}
	if err = r.service.repository.Apply(ctx, r.binding, []state.HistoryChange{{Expected: r.row.Version, Record: row}}); err != nil {
		return nil, err
	}
	r.row, r.content, r.terminal = row, content, true
	return event.JSON, nil
}

func (r *storedResponseRound) Finish(ctx context.Context, dispatched bool) error {
	if r.finished {
		return nil
	}
	if r.terminal {
		r.finished = true
		return nil
	}
	content := r.content
	content.Ready, content.Active = false, ""
	content.Request, content.Input, content.Items, content.Response = nil, nil, nil, nil
	row := r.row
	row.Version++
	row.Busy = ""
	if dispatched {
		row.Busy, content.Active = "uncertain", "uncertain"
	}
	row, err := r.service.repository.Seal(row, content)
	if err != nil {
		return err
	}
	if err = r.service.repository.Apply(ctx, r.binding, []state.HistoryChange{{Expected: r.row.Version, Record: row}}); err != nil {
		return err
	}
	r.row, r.content, r.finished = row, content, true
	return nil
}

func cloneHistoryItems(items []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = bytes.Clone(item)
	}
	return out
}

var _ history.StoredRounds = (*storedResponseRounds)(nil)
var _ history.StoredRound = (*storedResponseRound)(nil)
