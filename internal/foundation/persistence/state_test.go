package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/model"
)

type archiveFixture struct {
	ArchiveBackend
	record model.GenerationRecord
}

func (f *archiveFixture) CreateGenerationRecord(_ context.Context, in model.GenerationRecord, _ model.GenerationArchiveLimits) error {
	in.Segments[0].Ciphertext[0] = 91
	f.record = in
	return nil
}

func (f *archiveFixture) ReadGenerationRecord(context.Context, identity.Binding, string) (model.GenerationRecord, error) {
	return f.record, nil
}

func TestArchiveBridgeOwnsNestedCiphertextInBothDirections(t *testing.T) {
	backend := &archiveFixture{}
	port := Archive(backend)
	in := state.GenerationRecord{ID: "record", UserID: 8, TokenID: 9, State: "sealed", MaxBytes: 500, StoredBytes: 3, NextSequence: 1, Committed: true, CreatedAt: 11, ExpiresAt: 12,
		Segments: []state.GenerationSegment{{RecordID: "record", Sequence: 0, Kind: "result", Ciphertext: []byte{1, 2, 3}}}}
	if err := port.CreateGenerationRecord(context.Background(), in, state.GenerationArchiveLimits{}); err != nil {
		t.Fatal(err)
	}
	if in.Segments[0].Ciphertext[0] != 1 {
		t.Fatal("storage callback changed the caller's authenticated bytes")
	}
	out, err := port.ReadGenerationRecord(context.Background(), identity.Binding{UserID: 8, TokenID: 9}, "record")
	if err != nil || out.ID != in.ID || out.NextSequence != 1 || !out.Committed || out.StoredBytes != 3 || out.CreatedAt != 11 || out.ExpiresAt != 12 {
		t.Fatalf("archive ownership or sequence facts changed: %+v %v", out, err)
	}
	out.Segments[0].Ciphertext[1] = 92
	out.Segments[0].Kind = "body"
	if backend.record.Segments[0].Ciphertext[1] != 2 || backend.record.Segments[0].Kind != "result" {
		t.Fatal("public snapshot changed backend data")
	}
}

type leaseFixture struct {
	LeaseBackend
	intent model.GenerationIntent
	claim  model.GenerationClaim
}

func (f *leaseFixture) ClaimGenerationIntent(context.Context, time.Duration) (model.GenerationIntent, model.GenerationClaim, error) {
	return f.intent, f.claim, state.ErrClaimLost
}

func TestClaimBridgePreservesFencingFinancialAndUnknownFacts(t *testing.T) {
	backend := &leaseFixture{intent: model.GenerationIntent{ID: "intent", UserID: 4, TokenID: 5, AdmissionKey: "scope", RequestDigest: "digest", DigestVersion: "version", Operation: "chat", Model: "m", BillingID: "original-hold", State: model.IntentUncertain,
		Ciphertext: []byte{7, 8}, CreatedAt: 1, Deadline: 2, ExpiresAt: 3, ProviderPolicyVersion: "original-price", ClaimEpoch: 17, ClaimHash: "hash", ClaimUntil: 19, CancelRequested: true, OutputRecordID: "original-output"},
		claim: model.GenerationClaim{ID: "intent", Epoch: 17, Token: "capability"}}
	out, claim, err := Leases(backend).ClaimGenerationIntent(context.Background(), time.Second)
	if !errors.Is(err, state.ErrClaimLost) || claim.ID != out.ID || claim.Epoch != 17 || claim.Token != "capability" || out.BillingID != "original-hold" || out.ProviderPolicyVersion != "original-price" ||
		out.ClaimHash != "hash" || out.ClaimEpoch != 17 || out.ClaimUntil != 19 || !out.CancelRequested || out.OutputRecordID != "original-output" || out.State != state.IntentUncertain {
		t.Fatalf("claim bridge lost safety facts or error: %+v %+v %v", out, claim, err)
	}
	out.Ciphertext[0] = 0
	if backend.intent.Ciphertext[0] != 7 {
		t.Fatal("claim payload was not an owned snapshot")
	}
}

func TestStateBridgesRejectTypedNilBackends(t *testing.T) {
	var archive *archiveFixture
	var leases *leaseFixture
	if Archive(archive) != nil || Leases(leases) != nil {
		t.Fatal("typed nil was hidden inside a valid-looking adapter")
	}
}
