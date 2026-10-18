package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

type HistoryStore interface {
	ReadResponseHistory(context.Context, access.Binding, string) (model.ResponseHistory, error)
	ApplyResponseHistory(context.Context, access.Binding, []model.HistoryChange, []model.HistoryGuard, model.HistoryLimits) error
}

type HistoryOptions struct {
	Store     HistoryStore
	ActiveKey string
	Keys      map[string][]byte
	Retention time.Duration
	Limits    model.HistoryLimits
}

type History struct {
	store     HistoryStore
	ring      *secrets.Ring
	retention time.Duration
	limits    model.HistoryLimits
}

type historyContent struct {
	Request      json.RawMessage        `json:"request,omitempty"`
	Ready        bool                   `json:"ready"`
	Active       string                 `json:"active,omitempty"`
	Input        []json.RawMessage      `json:"input,omitempty"`
	Items        []json.RawMessage      `json:"items,omitempty"`
	Response     json.RawMessage        `json:"response,omitempty"`
	Metadata     map[string]string      `json:"metadata,omitempty"`
	Parent       string                 `json:"parent,omitempty"`
	Conversation string                 `json:"conversation,omitempty"`
	Target       gateway.ResponseTarget `json:"target"`
	PriceVersion string                 `json:"price_version,omitempty"`
	BillingID    string                 `json:"billing_id,omitempty"`
}

func NewHistory(opts HistoryOptions) (*History, error) {
	if missing(opts.Store) || opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords < opts.Limits.OwnerRecords || opts.Limits.OwnerBytes < model.ResponseHistoryMaxBytes || opts.Limits.InstanceBytes < opts.Limits.OwnerBytes || opts.Limits.InstanceBytes > 1<<30 {
		return nil, errors.New("invalid Responses history configuration")
	}
	ring, err := secrets.NewRing(opts.ActiveKey, opts.Keys)
	if err != nil {
		return nil, err
	}
	return &History{store: opts.Store, ring: ring, retention: opts.Retention, limits: opts.Limits}, nil
}

func historyIdentity(r model.ResponseHistory) []byte {
	return []byte(fmt.Sprintf("TXAPI/response-history/v1/%s/%d/%d/%s/%s/%d/%d/%d/%d/%t/", r.ID, r.UserID, r.TokenID, r.Kind, r.Model, r.Version, r.CreatedAt, r.ExpiresAt, r.ReservedBytes, r.Deleted))
}

func (h *History) read(ctx context.Context, b access.Binding, id string) (r model.ResponseHistory, c historyContent, err error) {
	r, err = h.store.ReadResponseHistory(ctx, b, id)
	if err != nil {
		return
	}
	if len(r.Ciphertext) == 0 || len(r.Ciphertext) > model.ResponseHistoryMaxBytes {
		err = model.ErrHistoryNotFound
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

func (h *History) seal(r model.ResponseHistory, c historyContent) (model.ResponseHistory, error) {
	plain, err := json.Marshal(c)
	if err != nil {
		return r, err
	}
	defer clear(plain)
	if len(plain) > model.ResponseHistoryMaxBytes-128 {
		return r, model.ErrHistoryCapacity
	}
	if r.Busy == "" || r.Busy == "uncertain" {
		r.ReservedBytes = int64(len(plain) + 2 + len(h.ring.Active()) + 12 + 16)
	} else {
		r.ReservedBytes = model.ResponseHistoryMaxBytes
	}
	r.Ciphertext, err = h.ring.Seal(plain, historyIdentity(r))
	if err != nil {
		return r, err
	}
	return r, nil
}

func (h *History) fresh(id, kind, name string, b access.Binding) model.ResponseHistory {
	now := time.Now()
	return model.ResponseHistory{ID: id, UserID: b.UserID, TokenID: b.TokenID, Kind: kind, Model: name, Version: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(h.retention).UnixMilli()}
}

func (h *History) apply(ctx context.Context, b access.Binding, changes []model.HistoryChange, guards ...model.HistoryGuard) error {
	return h.store.ApplyResponseHistory(ctx, b, changes, guards, h.limits)
}
