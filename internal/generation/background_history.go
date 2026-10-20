package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
)

func backgroundHistoryError(err error) error {
	switch {
	case errors.Is(err, state.ErrHistoryNotFound), errors.Is(err, access.ErrUnauthorized):
		return bg.ErrNotFound
	case errors.Is(err, state.ErrHistoryConflict):
		return bg.ErrConflict
	case errors.Is(err, state.ErrHistoryCapacity):
		return bg.ErrCapacity
	default:
		return safeBackgroundError(err)
	}
}

func completeBackgroundTarget(t execv1.TargetConstraint) bool {
	return t.ChannelID != 0 && t.CredentialID != 0 && t.ActualModel != "" && t.Fingerprint != ""
}

func (s *BackgroundService) prepareHistory(ctx context.Context, binding identity.Binding, in backgroundPayload, parentID, modelName string) (backgroundPayload, []state.HistoryGuard, error) {
	if nilResponseHistoryDependency(s.history) || nilResponseHistoryDependency(s.compiler) {
		return backgroundPayload{}, nil, bg.ErrUnsupportedCapability
	}
	parent, content, err := s.history.Read(ctx, binding, parentID)
	if err != nil {
		return backgroundPayload{}, nil, backgroundHistoryError(err)
	}
	if parent.ID != parentID || parent.UserID != binding.UserID || parent.TokenID != binding.TokenID || parent.Kind != "response" || parent.Model != modelName || parent.Version == 0 || !content.Ready || parent.Busy != "" || content.Active != "" {
		return backgroundPayload{}, nil, bg.ErrConflict
	}
	if len(content.Items) > history.MaxContextItems || !completeBackgroundTarget(content.Target) {
		return backgroundPayload{}, nil, bg.ErrUnsupportedCapability
	}
	body, err := s.compiler.CompileBackground(bytes.Clone(in.Body), content.Items)
	if err != nil {
		return backgroundPayload{}, nil, bg.ErrUnsupportedCapability
	}
	if len(body) == 0 || len(body) > bs.MaxInput {
		return backgroundPayload{}, nil, bg.ErrCapacity
	}
	in.Body = bytes.Clone(body)
	target := content.Target
	in.Target, in.ParentID, in.ParentVersion = &target, parent.ID, parent.Version
	return in, []state.HistoryGuard{{ID: parent.ID, Version: parent.Version}}, nil
}

func (s *BackgroundService) openPayload(row bs.Record) (backgroundPayload, error) {
	plain, err := s.keys.Open(row.Ciphertext, backgroundAAD(row, "input"))
	if err != nil {
		return backgroundPayload{}, bg.ErrUnavailable
	}
	defer clear(plain)
	var payload backgroundPayload
	if len(plain) > bs.MaxInput || json.Unmarshal(plain, &payload) != nil || string(payload.Operation) != row.Operation || len(payload.Body) == 0 {
		return backgroundPayload{}, bg.ErrUnavailable
	}
	if payload.Target == nil {
		if payload.ParentID != "" || payload.ParentVersion != 0 {
			return backgroundPayload{}, bg.ErrUnavailable
		}
	} else if !completeBackgroundTarget(*payload.Target) || !strings.HasPrefix(payload.ParentID, "resp_") || len(payload.ParentID) > 80 || payload.ParentVersion == 0 {
		return backgroundPayload{}, bg.ErrUnavailable
	}
	if payload.PublishHistory {
		var fields map[string]json.RawMessage
		var store bool
		if payload.Operation != execv1.Responses || json.Unmarshal(payload.Body, &fields) != nil || json.Unmarshal(fields["store"], &store) != nil || !store {
			return backgroundPayload{}, bg.ErrUnavailable
		}
	}
	return payload, nil
}
