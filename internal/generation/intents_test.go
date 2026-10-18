package generation

import (
	"bytes"
	"context"
	"errors"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/protocol"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func intentOptions(st *store.Store, admission IntentAdmission) IntentOptions {
	return IntentOptions{Parser: protocol.BuiltinParser{}, Admission: admission, Resolve: st.ResolvePrincipal, ActiveKey: "content-1",
		Keys: map[string][]byte{"content-1": bytes.Repeat([]byte{41}, 32)}, DigestKey: bytes.Repeat([]byte{72}, 32),
		QueueTTL: 10 * time.Second, Retention: time.Hour,
		Limits: model.GenerationIntentLimits{InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 4 << 20, OwnerBytes: 2 << 20}}
}

func intentAcceptor(t *testing.T, opts IntentOptions) *IntentAcceptor {
	t.Helper()
	a, err := NewIntentAcceptor(opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func admitIntent(t *testing.T, a *IntentAcceptor, binding access.Binding, key string) IntentReceipt {
	t.Helper()
	receipt, err := a.Admit(context.Background(), binding, key, strictSocketExecution())
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func assertIntentBudget(t *testing.T, st *store.Store, intents, reservations int64, held int64) {
	t.Helper()
	var counts [3]int64
	for i, table := range []any{&model.GenerationIntent{}, &model.BillingReservation{}, &model.BillingAttempt{}} {
		if err := st.DB().Model(table).Count(&counts[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if counts != [3]int64{intents, reservations, 0} {
		t.Fatalf("intent/reservation/attempt counts: %+v", counts)
	}
	var balances []model.BillingBalance
	if err := st.DB().Find(&balances).Error; err != nil {
		t.Fatal(err)
	}
	for _, b := range balances {
		if b.UsedUnits != 0 || b.ReservedUnits != held {
			t.Fatalf("non-atomic/double hold: %+v want=%d", b, held)
		}
	}
	if held != 0 && len(balances) != 2 {
		t.Fatal("dual financial authority missing")
	}
}

func TestIntentAtomicAdmissionRetryAndCancellation(t *testing.T) {
	st, binding, otherKey := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	opts := intentOptions(st, engine)
	a := intentAcceptor(t, opts)
	receipt := admitIntent(t, a, binding, "first-admission")
	if receipt.State != model.IntentQueued || len(receipt.ID) != 64 || receipt.BillingID != receipt.ID[:32] {
		t.Fatalf("unowned admission: %+v", receipt)
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
	// Key ordering/whitespace normalize; numbers retain their original spelling.
	in := strictSocketExecution()
	in.Body = []byte(`{ "messages": [{"content":"hello","role":"user"}], "service_tier":"default", "max_completion_tokens":32, "model":"m" }`)
	duplicate, err := a.Admit(context.Background(), binding, "first-admission", in)
	if err != nil || duplicate != receipt {
		t.Fatalf("same scoped request made another receipt: %+v %v", duplicate, err)
	}
	in.Body = bytes.ReplaceAll(in.Body, []byte("hello"), []byte("different"))
	if _, err := a.Admit(context.Background(), binding, "first-admission", in); !errors.Is(err, model.ErrIntentConflict) {
		t.Fatalf("same key/different parameters: %v", err)
	}
	if _, _, err := a.Read(context.Background(), otherKey, receipt.ID); !errors.Is(err, model.ErrIntentNotFound) {
		t.Fatalf("another Key read intent: %v", err)
	}
	if _, err := a.Cancel(context.Background(), otherKey, receipt.ID); !errors.Is(err, model.ErrIntentNotFound) {
		t.Fatalf("another Key cancelled intent: %v", err)
	}
	if err := st.DB().Exec("CREATE TRIGGER fail_intent_cancel BEFORE UPDATE OF state ON generation_intents BEGIN SELECT RAISE(ABORT, 'controlled intent state failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Cancel(context.Background(), binding, receipt.ID); err == nil {
		t.Fatal("failed intent transition committed a release")
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
	if err := st.DB().Exec("DROP TRIGGER fail_intent_cancel").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		cancelled, err := a.Cancel(context.Background(), binding, receipt.ID)
		if err != nil || cancelled.State != model.IntentCancelled {
			t.Fatalf("cancel did not atomically settle: %+v %v", cancelled, err)
		}
	}
	assertIntentBudget(t, st, 1, 1, 0)
	if got := admitIntent(t, a, binding, "first-admission"); got.ID != receipt.ID || got.State != model.IntentCancelled {
		t.Fatalf("retry restarted cancelled work: %+v", got)
	}
	assertIntentBudget(t, st, 1, 1, 0)
}

func TestIntentInsertFailureRollsBackDualHoldAndConfigurationMarker(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	a := intentAcceptor(t, intentOptions(st, engine))
	if err := st.DB().Exec("CREATE TRIGGER fail_intent_insert BEFORE INSERT ON generation_intents BEGIN SELECT RAISE(ABORT, 'controlled intent insert failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.Admit(context.Background(), binding, "failed-insert", strictSocketExecution()); err == nil {
		t.Fatal("partial task admission was reported successful")
	}
	assertIntentBudget(t, st, 0, 0, 0)
	var count int64
	if err := st.DB().Model(&model.Setting{}).Where("key = ?", "generation.intent_digest_version").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed acceptance persisted configuration: %d %v", count, err)
	}
	var user model.User
	if err := st.DB().First(&user, binding.UserID).Error; err != nil || user.UsedQuota != 0 {
		t.Fatalf("compatibility projection escaped rollback: %+v %v", user, err)
	}
}

type lostIntentConfirmation struct {
	IntentAdmission
	lost atomic.Bool
}

func (p *lostIntentConfirmation) AdmitGenerationIntent(ctx context.Context, in model.GenerationIntent, limits model.GenerationIntentLimits) (model.GenerationIntent, error) {
	out, err := p.IntentAdmission.AdmitGenerationIntent(ctx, in, limits)
	if err == nil && p.lost.CompareAndSwap(false, true) {
		return model.GenerationIntent{}, errors.New("isolated post-commit confirmation loss")
	}
	return out, err
}

func TestIntentLostCommitConfirmationRetriesWithoutAnotherHold(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	a := intentAcceptor(t, intentOptions(st, &lostIntentConfirmation{IntentAdmission: engine}))
	if got, err := a.Admit(context.Background(), binding, "ambiguous-commit", strictSocketExecution()); err == nil || got.ID != "" {
		t.Fatalf("confirmation loss exposed a successful receipt: %+v %v", got, err)
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
	receipt := admitIntent(t, a, binding, "ambiguous-commit")
	if receipt.State != model.IntentQueued {
		t.Fatal(receipt)
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
}

func TestIntentConcurrentRetriesAndCancelRecoveryAreAtomic(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	a := intentAcceptor(t, intentOptions(st, engine))
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := a.Admit(context.Background(), binding, "concurrent", strictSocketExecution())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- receipt.ID
		}()
	}
	wg.Wait()
	close(ids)
	var id string
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("idempotency split under concurrent admission")
		}
		id = got
	}
	if id == "" {
		t.Fatal("no request admitted")
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
	for range 12 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := a.Cancel(context.Background(), binding, id)
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			err := engine.RecoverExpired(context.Background(), time.Now().Add(time.Minute))
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertIntentBudget(t, st, 1, 1, 0)
	if _, err := a.Cancel(context.Background(), binding, id); err != nil {
		t.Fatal(err)
	}
}

func TestIntentCurrentAuthorityCapacityAndExpiryRecovery(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	opts := intentOptions(st, engine)
	opts.Limits.OwnerRecords = 1
	a := intentAcceptor(t, opts)
	receipt := admitIntent(t, a, binding, "capacity")
	if _, err := a.Admit(context.Background(), binding, "other-admission", strictSocketExecution()); !errors.Is(err, model.ErrIntentCapacity) {
		t.Fatalf("owner capacity bypassed: %v", err)
	}
	if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Read(context.Background(), binding, receipt.ID); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("revoked owner read encrypted input: %v", err)
	}
	if _, err := a.Admit(context.Background(), binding, "capacity", strictSocketExecution()); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("revoked key replayed admission receipt: %v", err)
	}
	for range 2 {
		if err := engine.RecoverExpired(context.Background(), time.UnixMilli(receipt.Deadline+1)); err != nil {
			t.Fatal(err)
		}
	}
	assertIntentBudget(t, st, 1, 1, 0)
	var persisted model.GenerationIntent
	if err := st.DB().First(&persisted, "id = ?", receipt.ID).Error; err != nil || persisted.State != model.IntentExpired {
		t.Fatalf("recovery did not preserve expired receipt: %+v %v", persisted, err)
	}
}

func TestIntentCryptographicBindingRotationAndDigestKeyMismatch(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	opts := intentOptions(st, engine)
	a := intentAcceptor(t, opts)
	in := strictSocketExecution()
	in.Body = bytes.ReplaceAll(in.Body, []byte("hello"), []byte("input-marker-must-not-be-plaintext-513a"))
	in.SessionID = "private-session-marker-4dc1"
	receipt, err := a.Admit(context.Background(), binding, "private-idempotency-marker-7cf2", in)
	if err != nil {
		t.Fatal(err)
	}
	var stored model.GenerationIntent
	if err := st.DB().First(&stored, "id = ?", receipt.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"input-marker-must-not-be-plaintext-513a", in.SessionID, "private-idempotency-marker-7cf2"} {
		if bytes.Contains(stored.Ciphertext, []byte(marker)) || strings.Contains(stored.AdmissionKey+stored.RequestDigest, marker) {
			t.Fatal("sensitive task input was persisted without protection")
		}
	}
	for _, tamper := range []map[string]any{{"user_id": binding.UserID + 1}, {"billing_id": strings.Repeat("a", 32)}, {"model": "different"}, {"deadline": stored.Deadline + 1}, {"ciphertext": bytes.Repeat([]byte{1}, len(stored.Ciphertext))}} {
		copy := stored
		if _, ok := tamper["user_id"]; ok {
			copy.UserID++
		}
		if _, ok := tamper["billing_id"]; ok {
			copy.BillingID = strings.Repeat("a", 32)
		}
		if _, ok := tamper["model"]; ok {
			copy.Model = "different"
		}
		if _, ok := tamper["deadline"]; ok {
			copy.Deadline++
		}
		if _, ok := tamper["ciphertext"]; ok {
			copy.Ciphertext = bytes.Repeat([]byte{1}, len(copy.Ciphertext))
		}
		if _, err := a.openIntent(copy); !errors.Is(err, ErrIntentIntegrity) {
			t.Fatalf("tampered immutable metadata was accepted: %v", err)
		}
	}
	rotated := intentOptions(st, engine)
	rotated.ActiveKey = "content-2"
	rotated.Keys["content-2"] = bytes.Repeat([]byte{42}, 32)
	rotated.QueueTTL = 5 * time.Second
	reader := intentAcceptor(t, rotated)
	if got, body, err := reader.Read(context.Background(), binding, receipt.ID); err != nil || got.ID != receipt.ID || !bytes.Contains(body.Body, []byte("input-marker")) || body.SessionID != in.SessionID {
		t.Fatalf("AEAD rotation/config refresh lost old input: %+v %v", got, err)
	}
	if got, err := reader.Admit(context.Background(), binding, "private-idempotency-marker-7cf2", in); err != nil || got != receipt {
		t.Fatalf("rotation changed idempotency: %+v %v", got, err)
	}
	wrong := intentOptions(st, engine)
	wrong.DigestKey = bytes.Repeat([]byte{73}, 32)
	wrongReader := intentAcceptor(t, wrong)
	if _, err := wrongReader.Admit(context.Background(), binding, "private-idempotency-marker-7cf2", in); !errors.Is(err, model.ErrIntentConflict) {
		t.Fatalf("changed digest secret created another idempotency namespace: %v", err)
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
	delete(rotated.Keys, "content-1")
	if _, _, err := intentAcceptor(t, rotated).Read(context.Background(), binding, receipt.ID); !errors.Is(err, ErrIntentIntegrity) {
		t.Fatalf("missing historical key returned plaintext: %v", err)
	}
}

func TestIntentInputSnapshotBeforeAuthorityCallbackAndValidation(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	opts := intentOptions(st, engine)
	in := strictSocketExecution()
	opts.Resolve = func(ctx context.Context, b access.Binding) (access.Principal, error) {
		for i := range in.Body {
			in.Body[i] = 'x'
		}
		return st.ResolvePrincipal(ctx, b)
	}
	a := intentAcceptor(t, opts)
	receipt, err := a.Admit(context.Background(), binding, "snapshot", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, got, err := a.Read(context.Background(), binding, receipt.ID); err != nil || !bytes.Contains(got.Body, []byte("hello")) {
		t.Fatalf("authority callback changed saved request: %s %v", got.Body, err)
	}
	for _, body := range [][]byte{[]byte(`{"model":"m","model":"other"}`), []byte(`{"model":"m","messages":[{"role":"user","content":"a","content":"b"}]}`), []byte("null"), {255}, bytes.Repeat([]byte("x"), 65537)} {
		if _, err := a.Admit(context.Background(), binding, "invalid", execv1.Invocation{Operation: "chat", Body: body}); err == nil {
			t.Fatal("ambiguous/oversized input was accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Admit(ctx, binding, "cancelled", strictSocketExecution()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertIntentBudget(t, st, 1, 1, billing.UnitScale)
}

func TestIntentKeyScopesAndNarrowedModelAuthorityRemainIndependent(t *testing.T) {
	st, binding, otherKey := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	a := intentAcceptor(t, intentOptions(st, engine))
	first := admitIntent(t, a, binding, "shared-idempotency-key")
	second := admitIntent(t, a, otherKey, "shared-idempotency-key")
	if first.ID == second.ID || first.BillingID == second.BillingID {
		t.Fatal("another Key inherited an existing admission or bill")
	}
	if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Updates(map[string]any{"model_mode": access.Allowlist, "models": model.StringList{}}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Read(context.Background(), binding, first.ID); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("model policy shrink exposed persisted request: %v", err)
	}
	if _, err := a.Admit(context.Background(), binding, "shared-idempotency-key", strictSocketExecution()); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("retry used admission-time authority: %v", err)
	}
	if got, _, err := a.Read(context.Background(), otherKey, second.ID); err != nil || got.ID != second.ID {
		t.Fatalf("other Key's authorized receipt changed: %+v %v", got, err)
	}
}

func TestIntentInvalidConfigurationFailsClosed(t *testing.T) {
	st, _, _ := fixture(t)
	engine, _ := strictRecordingFixture(t, st)
	for _, mutate := range []func(*IntentOptions){
		func(o *IntentOptions) { o.Admission = nil },
		func(o *IntentOptions) { var nilEngine *billing.Engine; o.Admission = nilEngine },
		func(o *IntentOptions) { o.Resolve = nil },
		func(o *IntentOptions) { o.Parser = nil },
		func(o *IntentOptions) { var parser *protocol.BuiltinParser; o.Parser = parser },
		func(o *IntentOptions) { o.DigestKey = o.Keys[o.ActiveKey] },
		func(o *IntentOptions) { o.DigestKey = nil },
		func(o *IntentOptions) { o.QueueTTL = 0 },
		func(o *IntentOptions) { o.Retention = 30 * time.Second },
		func(o *IntentOptions) { o.Limits.OwnerRecords = o.Limits.InstanceRecords },
		func(o *IntentOptions) { o.Limits.InstanceBytes = 65 << 20 },
	} {
		opts := intentOptions(st, engine)
		mutate(&opts)
		if _, err := NewIntentAcceptor(opts); err == nil {
			t.Fatal("invalid/unbounded intent configuration accepted")
		}
	}
}

// Keep this small check separate from the subprocess crash cases: files at rest
// must not contain the request or caller's original idempotency key.
func assertIntentDatabaseNoPlaintext(t *testing.T, path string, markers ...string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, marker := range markers {
			if bytes.Contains(data, []byte(marker)) {
				t.Fatalf("plaintext task content in %s", suffix)
			}
		}
	}
}
