package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"time"

	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

type HistoryStore = state.HistoryStore

type HistoryOptions struct {
	Store     HistoryStore
	Protector state.ContentProtection
	Retention time.Duration
	Limits    state.HistoryLimits
}

type History struct {
	store     HistoryStore
	ring      state.ContentProtection
	retention time.Duration
	limits    state.HistoryLimits
}

type historyContent struct {
	Request      json.RawMessage         `json:"request,omitempty"`
	Ready        bool                    `json:"ready"`
	Active       string                  `json:"active,omitempty"`
	Input        []json.RawMessage       `json:"input,omitempty"`
	Items        []json.RawMessage       `json:"items,omitempty"`
	Response     json.RawMessage         `json:"response,omitempty"`
	Metadata     map[string]string       `json:"metadata,omitempty"`
	Parent       string                  `json:"parent,omitempty"`
	Conversation string                  `json:"conversation,omitempty"`
	Target       execv1.TargetConstraint `json:"target"`
	PriceVersion string                  `json:"price_version,omitempty"`
	BillingID    string                  `json:"billing_id,omitempty"`
}

func NewHistory(opts HistoryOptions) (*History, error) {
	if missing(opts.Store) || opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords < opts.Limits.OwnerRecords || opts.Limits.OwnerBytes < state.ResponseHistoryMaxBytes || opts.Limits.InstanceBytes < opts.Limits.OwnerBytes || opts.Limits.InstanceBytes > 1<<30 {
		return nil, errors.New("invalid Responses history configuration")
	}
	if missing(opts.Protector) {
		return nil, errors.New("Responses content protection is required")
	}
	ring := opts.Protector
	return &History{store: opts.Store, ring: ring, retention: opts.Retention, limits: opts.Limits}, nil
}

func historyIdentity(r state.ResponseHistory) []byte {
	return []byte(fmt.Sprintf("TXAPI/response-history/v1/%s/%d/%d/%s/%s/%d/%d/%d/%d/%t/", r.ID, r.UserID, r.TokenID, r.Kind, r.Model, r.Version, r.CreatedAt, r.ExpiresAt, r.ReservedBytes, r.Deleted))
}

func (h *History) read(ctx context.Context, b identity.Binding, id string) (r state.ResponseHistory, c historyContent, err error) {
	r, err = h.store.ReadResponseHistory(ctx, b, id)
	if err != nil {
		return
	}
	if len(r.Ciphertext) == 0 || len(r.Ciphertext) > state.ResponseHistoryMaxBytes {
		err = state.ErrHistoryNotFound
		return
	}
	var plain []byte
	plain, err = h.ring.Open(r.Ciphertext, historyIdentity(r))
	if err != nil {
		return
	}
	defer clear(plain)
	err = json.Unmarshal(plain, &c)
	return
}

func (h *History) seal(r state.ResponseHistory, c historyContent) (state.ResponseHistory, error) {
	plain, err := json.Marshal(c)
	if err != nil {
		return r, err
	}
	defer clear(plain)
	if len(plain) > state.ResponseHistoryMaxBytes-128 {
		return r, state.ErrHistoryCapacity
	}
	if r.Busy == "" || r.Busy == "uncertain" {
		r.ReservedBytes = int64(len(plain) + 2 + len(h.ring.Active()) + 12 + 16)
	} else {
		r.ReservedBytes = state.ResponseHistoryMaxBytes
	}
	r.Ciphertext, err = h.ring.Seal(plain, historyIdentity(r))
	if err != nil {
		return r, err
	}
	return r, nil
}

func (h *History) fresh(id, kind, name string, b identity.Binding) state.ResponseHistory {
	now := time.Now()
	return state.ResponseHistory{ID: id, UserID: b.UserID, TokenID: b.TokenID, Kind: kind, Model: name, Version: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(h.retention).UnixMilli()}
}

func (h *History) apply(ctx context.Context, b identity.Binding, changes []state.HistoryChange, guards ...state.HistoryGuard) error {
	return h.store.ApplyResponseHistory(ctx, b, changes, guards, h.limits)
}
