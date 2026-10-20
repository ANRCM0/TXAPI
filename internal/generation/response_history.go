package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

type ResponseHistoryOptions struct {
	Store     state.HistoryStore
	Protector state.ContentProtection
	Retention time.Duration
	Limits    state.HistoryLimits
}

type responseHistory struct {
	store     state.HistoryStore
	ring      state.ContentProtection
	retention time.Duration
	limits    state.HistoryLimits
}

func NewResponseHistory(opts ResponseHistoryOptions) (history.Repository, error) {
	if nilResponseHistoryDependency(opts.Store) || opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords < opts.Limits.OwnerRecords || opts.Limits.OwnerBytes < state.ResponseHistoryMaxBytes || opts.Limits.InstanceBytes < opts.Limits.OwnerBytes || opts.Limits.InstanceBytes > 1<<30 {
		return nil, errors.New("invalid Responses history configuration")
	}
	if nilResponseHistoryDependency(opts.Protector) {
		return nil, errors.New("Responses content protection is required")
	}
	ring := opts.Protector
	return &responseHistory{store: opts.Store, ring: ring, retention: opts.Retention, limits: opts.Limits}, nil
}

func historyIdentity(r state.ResponseHistory) []byte {
	return []byte(fmt.Sprintf("TXAPI/response-history/v1/%s/%d/%d/%s/%s/%d/%d/%d/%d/%t/", r.ID, r.UserID, r.TokenID, r.Kind, r.Model, r.Version, r.CreatedAt, r.ExpiresAt, r.ReservedBytes, r.Deleted))
}

func (h *responseHistory) Read(ctx context.Context, b identity.Binding, id string) (r state.ResponseHistory, c history.Content, err error) {
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

func (h *responseHistory) Seal(r state.ResponseHistory, c history.Content) (state.ResponseHistory, error) {
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

func (h *responseHistory) Fresh(id, kind, name string, b identity.Binding) state.ResponseHistory {
	now := time.Now()
	return state.ResponseHistory{ID: id, UserID: b.UserID, TokenID: b.TokenID, Kind: kind, Model: name, Version: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(h.retention).UnixMilli()}
}

func (h *responseHistory) Limits() state.HistoryLimits { return h.limits }

func (h *responseHistory) Apply(ctx context.Context, b identity.Binding, changes []state.HistoryChange, guards ...state.HistoryGuard) error {
	return h.store.ApplyResponseHistory(ctx, b, changes, guards, h.limits)
}

// Interface implementations may be typed nil values at assembly boundaries.
func nilResponseHistoryDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}

var _ history.Repository = (*responseHistory)(nil)
