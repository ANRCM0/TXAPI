package generation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/generation"
	"minigateway/internal/secrets"
)

// Authorization, CAS, TTL and quotas are exercised by the existing HTTP tests
// against SQLite. This seam isolates compatibility of the encrypted format.
type historyCompatibilityStore struct{ row state.ResponseHistory }

func (s *historyCompatibilityStore) ReadResponseHistory(context.Context, identity.Binding, string) (state.ResponseHistory, error) {
	return s.row, nil
}
func (s *historyCompatibilityStore) ApplyResponseHistory(context.Context, identity.Binding, []state.HistoryChange, []state.HistoryGuard, state.HistoryLimits) error {
	return errors.New("compatibility fixture must not persist")
}

func TestResponseHistoryOwnerLegacyCiphertextRoundTrip(t *testing.T) {
	oldKeys := map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)}
	oldRing, err := secrets.NewRing("old", oldKeys)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string][]byte{"old": oldKeys["old"], "new": bytes.Repeat([]byte{32}, 32)}
	ring, err := secrets.NewRing("new", keys)
	if err != nil {
		t.Fatal(err)
	}
	store := &historyCompatibilityStore{row: state.ResponseHistory{ID: "resp_legacy", UserID: 7, TokenID: 11, Kind: "response", Model: "m", Version: 1, CreatedAt: 1700000000000, ExpiresAt: 1700000060000, ReservedBytes: 2048}}
	// Literal JSON and AAD describe the pre-extraction persisted format without
	// using the new Content type or its identity helper to build the fixture.
	legacy := []byte(`{"request":{"model":"m","input":[]},"ready":true,"input":[{"id":"input_1"}],"items":[{"id":"output_1"}],"response":{"id":"resp_legacy","output":[]},"metadata":{"fixture":"private"},"parent":"resp_parent","conversation":"conv_legacy","target":{"channel_id":1,"credential_id":2,"actual_model":"native-m","fingerprint":"legacy-pin"},"price_version":"price","billing_id":"bill"}`)
	store.row.Ciphertext, err = oldRing.Seal(legacy, []byte("TXAPI/response-history/v1/resp_legacy/7/11/response/m/1/1700000000000/1700000060000/2048/false/"))
	if err != nil {
		t.Fatal(err)
	}
	opts := generation.ResponseHistoryOptions{Store: store, Protector: ring, Retention: time.Minute, Limits: state.HistoryLimits{OwnerRecords: 2, InstanceRecords: 4, OwnerBytes: 4 << 20, InstanceBytes: 8 << 20}}
	repository, err := generation.NewResponseHistory(opts)
	if err != nil {
		t.Fatal(err)
	}
	row, content, err := repository.Read(context.Background(), identity.Binding{UserID: 7, TokenID: 11}, store.row.ID)
	if err != nil || !content.Ready || content.Target.Fingerprint != "legacy-pin" || content.Target.ChannelID != 1 || content.Target.CredentialID != 2 || content.Target.ActualModel != "native-m" || content.Parent != "resp_parent" || content.BillingID != "bill" {
		t.Fatal("legacy content unavailable", err, content)
	}
	row.Version++
	sealed, err := repository.Seal(row, content)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("private")) {
		t.Fatal("plaintext leaked")
	}
	// A reader using the old key-ring API and old AAD format can still open a
	// new document. Retained old keys permit reading the pre-rotation record.
	aad := []byte(fmt.Sprintf("TXAPI/response-history/v1/resp_legacy/7/11/response/m/2/1700000000000/1700000060000/%d/false/", sealed.ReservedBytes))
	plain, err := ring.Open(sealed.Ciphertext, aad)
	if err != nil {
		t.Fatal("new content broke old reader format", err)
	}
	defer clear(plain)
	var before, after map[string]json.RawMessage
	if json.Unmarshal(legacy, &before) != nil || json.Unmarshal(plain, &after) != nil {
		t.Fatal("invalid fixture document")
	}
	// TargetConstraint already has optional zero-valued fields in its existing
	// JSON representation; compare its decoded value separately.
	delete(before, "target")
	delete(after, "target")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("document changed across owner migration: %s", plain)
	}
	if _, err := oldRing.Open(sealed.Ciphertext, aad); err == nil {
		t.Fatal("new write failed to rotate key")
	}
	store.row.TokenID++
	if _, _, err := repository.Read(context.Background(), identity.Binding{UserID: 7, TokenID: 12}, store.row.ID); !errors.Is(err, secrets.ErrIntegrity) {
		t.Fatal("altered Key identity was not authenticated", err)
	}
	if _, err := repository.Seal(row, history.Content{Request: json.RawMessage(`"` + strings.Repeat("x", state.ResponseHistoryMaxBytes) + `"`)}); !errors.Is(err, state.ErrHistoryCapacity) {
		t.Fatal("oversized document was accepted", err)
	}
	var absent *historyCompatibilityStore
	opts.Store = absent
	if _, err := generation.NewResponseHistory(opts); err == nil {
		t.Fatal("typed nil persistence accepted")
	}
	opts.Store = store
	var absentRing *secrets.Ring
	opts.Protector = absentRing
	if _, err := generation.NewResponseHistory(opts); err == nil {
		t.Fatal("typed nil protection accepted")
	}
}
