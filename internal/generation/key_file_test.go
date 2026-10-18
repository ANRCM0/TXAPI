package generation

import (
	"bytes"
	"context"
	"testing"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

func TestContentKeyRequirementsIncludeInputOutputAndStableDigest(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	opts := intentOptions(st, engine)
	a := intentAcceptor(t, opts)
	admitIntent(t, a, binding, "required-input-key")
	archive := testArchive(t, archiveOptions(st))
	output, err := archive.start(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Commit(context.Background(), execv1.ResponseMetadata{StatusCode: 200, ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	requirements, err := st.GenerationKeyRequirements(context.Background())
	if err != nil || len(requirements) != 2 {
		t.Fatalf("bounded SQL header scan incorrect: count=%d %v", len(requirements), err)
	}
	version, err := st.GetSetting("generation.intent_digest_version")
	if err != nil {
		t.Fatal(err)
	}
	keys := ContentKeyConfig{Active: opts.ActiveKey, Keys: map[string][]byte{opts.ActiveKey: bytes.Repeat([]byte{41}, 32), "key-1": bytes.Repeat([]byte{21}, 32)}, Digest: opts.DigestKey}
	if err := keys.ValidateRequirements(requirements, version); err != nil {
		t.Fatal("retained headers rejected", err)
	}
	delete(keys.Keys, "key-1")
	if err := keys.ValidateRequirements(requirements, version); err == nil {
		t.Fatal("missing output key accepted")
	}
	keys.Keys["key-1"] = bytes.Repeat([]byte{21}, 32)
	keys.Digest = bytes.Repeat([]byte{73}, 32)
	if err := keys.ValidateRequirements(nil, version); err == nil {
		t.Fatal("empty content reset global idempotency secret")
	}
	keys.Digest = opts.DigestKey
	if err := st.DB().Model(&model.GenerationIntent{}).Where("1 = 1").Update("ciphertext", []byte{1, 255, 1, 2, 3}).Error; err != nil {
		t.Fatal(err)
	}
	requirements, err = st.GenerationKeyRequirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.ValidateRequirements(requirements, version); err == nil {
		t.Fatal("corrupt unbounded header accepted")
	}
}
