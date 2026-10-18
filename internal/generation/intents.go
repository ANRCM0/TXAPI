package generation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"time"
	"unicode/utf8"

	"minigateway/internal/access"
	jsonutil "minigateway/internal/foundation/contracts/json/v1"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

var ErrIntentIntegrity = errors.New("generation intent cannot be authenticated")

// IntentAdmission is a narrow transaction port, not a raw DB callback or another
// financial ledger. The implementation owns both the intent and dual hold.
type IntentAdmission interface {
	AdmitGenerationIntent(context.Context, model.GenerationIntent, model.GenerationIntentLimits) (model.GenerationIntent, error)
	ReadGenerationIntent(context.Context, uint, uint, string) (model.GenerationIntent, error)
	CancelGenerationIntent(context.Context, uint, uint, string) (model.GenerationIntent, error)
}

type IntentOptions struct {
	Parser    execv1.Parser
	Admission IntentAdmission
	Resolve   func(context.Context, access.Binding) (access.Principal, error)
	ActiveKey string
	Keys      map[string][]byte
	// A separate stable secret protects low-entropy request/admission digests.
	// Keep it across restarts/AEAD rotation for the full idempotency lifetime.
	DigestKey []byte
	QueueTTL  time.Duration
	Retention time.Duration
	Limits    model.GenerationIntentLimits
}

type IntentAcceptor struct {
	parser    execv1.Parser
	admission IntentAdmission
	resolve   func(context.Context, access.Binding) (access.Principal, error)
	keys      *secrets.Ring
	digestKey []byte
	queueTTL  time.Duration
	retention time.Duration
	limits    model.GenerationIntentLimits
}

type IntentReceipt struct {
	ID, BillingID, State           string
	OutputRecordID                 string
	CreatedAt, Deadline, ExpiresAt int64
}

type intentPayload struct {
	Operation execv1.Operation
	Body      json.RawMessage
	SessionID string
}

func NewIntentAcceptor(opts IntentOptions) (*IntentAcceptor, error) {
	if nilPort(opts.Parser) || nilPort(opts.Admission) || opts.Resolve == nil || len(opts.DigestKey) != 32 ||
		opts.QueueTTL < time.Second || opts.QueueTTL > 5*time.Minute || opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Retention < opts.QueueTTL ||
		opts.Limits.InstanceRecords < 2 || opts.Limits.InstanceRecords > 4096 || opts.Limits.OwnerRecords < 1 || opts.Limits.OwnerRecords >= opts.Limits.InstanceRecords ||
		opts.Limits.OwnerBytes < 68<<10 || opts.Limits.InstanceBytes < opts.Limits.OwnerBytes || opts.Limits.InstanceBytes > 64<<20 {
		return nil, fmt.Errorf("invalid generation intent configuration")
	}
	keys, err := secrets.NewRing(opts.ActiveKey, opts.Keys)
	if err != nil {
		return nil, err
	}
	for _, key := range opts.Keys {
		if bytes.Equal(opts.DigestKey, key) {
			return nil, fmt.Errorf("generation digest and content keys must be independent")
		}
	}
	return &IntentAcceptor{parser: opts.Parser, admission: opts.Admission, resolve: opts.Resolve, keys: keys,
		digestKey: bytes.Clone(opts.DigestKey), queueTTL: opts.QueueTTL, retention: opts.Retention, limits: opts.Limits}, nil
}

func (a *IntentAcceptor) digest(domain string, data []byte) string {
	mac := hmac.New(sha256.New, a.digestKey)
	_, _ = mac.Write([]byte("TXAPI/intent/v1/" + domain + "/"))
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func canonicalIntent(in execv1.Invocation) (intentPayload, []byte, error) {
	if len(in.Body) == 0 || len(in.Body) > 64<<10 || len(in.SessionID) > 256 || !utf8.Valid(in.Body) || !utf8.ValidString(in.SessionID) {
		return intentPayload{}, nil, model.ErrIntentCapacity
	}
	if err := jsonutil.Validate(in.Body); err != nil {
		return intentPayload{}, nil, model.ErrIntentConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(in.Body))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return intentPayload{}, nil, model.ErrIntentConflict
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return intentPayload{}, nil, model.ErrIntentConflict
	}
	payload := intentPayload{Operation: in.Operation, Body: body, SessionID: in.SessionID}
	plain, err := json.Marshal(payload)
	return payload, plain, err
}

func intentAAD(in model.GenerationIntent, header []byte) []byte {
	meta, _ := json.Marshal(struct {
		Domain, ID, AdmissionKey, Digest, DigestVersion, Operation, Model, BillingID string
		UserID, TokenID                                                              uint
		CreatedAt, Deadline, ExpiresAt                                               int64
	}{"TXAPI/generation-intent/v1", in.ID, in.AdmissionKey, in.RequestDigest, in.DigestVersion, in.Operation, in.Model, in.BillingID,
		in.UserID, in.TokenID, in.CreatedAt, in.Deadline, in.ExpiresAt})
	return append(meta, header...)
}

func (a *IntentAcceptor) sealIntent(in model.GenerationIntent, plain []byte) ([]byte, error) {
	sealed, err := a.keys.Seal(plain, intentAAD(in, nil))
	if err != nil {
		return nil, ErrIntentIntegrity
	}
	return sealed, nil
}

func (a *IntentAcceptor) openIntent(in model.GenerationIntent) (execv1.Invocation, error) {
	version := sha256.Sum256(a.digestKey)
	if in.DigestVersion != hex.EncodeToString(version[:]) || !validIntentState(in.State) {
		return execv1.Invocation{}, ErrIntentIntegrity
	}
	data := in.Ciphertext
	if len(data) > 68<<10 {
		return execv1.Invocation{}, ErrIntentIntegrity
	}
	plain, err := a.keys.Open(data, intentAAD(in, nil))
	if err != nil {
		return execv1.Invocation{}, ErrIntentIntegrity
	}
	var payload intentPayload
	if json.Unmarshal(plain, &payload) != nil || in.RequestDigest != a.requestDigest(plain) || string(payload.Operation) != in.Operation {
		return execv1.Invocation{}, ErrIntentIntegrity
	}
	req, err := a.parser.ParseRequest(payload.Operation, payload.Body)
	if err != nil || nilPort(req) || req.Model() != in.Model {
		return execv1.Invocation{}, ErrIntentIntegrity
	}
	return execv1.Invocation{Operation: payload.Operation, Body: payload.Body, SessionID: payload.SessionID}, nil
}

func validIntentState(state string) bool {
	switch state {
	case model.IntentQueued, model.IntentClaimed, model.IntentRunning, model.IntentFinalizing, model.IntentSucceeded, model.IntentFailed, model.IntentCancelled, model.IntentExpired, model.IntentUncertain:
		return true
	default:
		return false
	}
}

func (a *IntentAcceptor) requestDigest(plain []byte) string {
	return a.digest("request", plain)
}

// Admit persists an encrypted intent and dual budget hold before returning.
// It neither enqueues Runner work nor authorizes an upstream/background send.
func (a *IntentAcceptor) Admit(ctx context.Context, binding access.Binding, idempotencyKey string, in execv1.Invocation) (IntentReceipt, error) {
	if err := ctx.Err(); err != nil {
		return IntentReceipt{}, err
	}
	if len(idempotencyKey) < 1 || len(idempotencyKey) > 128 {
		return IntentReceipt{}, model.ErrIntentConflict
	}
	for _, c := range idempotencyKey {
		if c < 33 || c > 126 {
			return IntentReceipt{}, model.ErrIntentConflict
		}
	}
	// Snapshot before invoking trusted authority or persistence callbacks.
	payload, plain, err := canonicalIntent(in)
	if err != nil {
		return IntentReceipt{}, err
	}
	req, err := a.parser.ParseRequest(payload.Operation, payload.Body)
	if err != nil || nilPort(req) || len(req.Model()) > 256 {
		return IntentReceipt{}, model.ErrIntentConflict
	}
	principal, err := currentPrincipal(ctx, a.resolve, binding)
	if err != nil {
		return IntentReceipt{}, err
	}
	if !principal.Policy.Allows(req.Model()) {
		return IntentReceipt{}, access.ErrUnauthorized
	}
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return IntentReceipt{}, ErrIntentIntegrity
	}
	now := time.Now()
	intent := model.GenerationIntent{ID: hex.EncodeToString(id[:]), UserID: binding.UserID, TokenID: binding.TokenID,
		AdmissionKey: a.digest("admission", []byte(idempotencyKey)), RequestDigest: a.requestDigest(plain), Operation: string(payload.Operation), Model: req.Model(),
		State: model.IntentQueued, CreatedAt: now.UnixMilli(), Deadline: now.Add(a.queueTTL).UnixMilli(), ExpiresAt: now.Add(a.retention).UnixMilli()}
	version := sha256.Sum256(a.digestKey)
	intent.DigestVersion = hex.EncodeToString(version[:])
	intent.BillingID = intent.ID[:32]
	intent.Ciphertext, err = a.sealIntent(intent, plain)
	if err != nil {
		return IntentReceipt{}, err
	}
	stored, err := a.admission.AdmitGenerationIntent(ctx, intent, a.limits)
	if err != nil {
		return IntentReceipt{}, err
	}
	if stored.UserID != binding.UserID || stored.TokenID != binding.TokenID || stored.AdmissionKey != intent.AdmissionKey || stored.RequestDigest != intent.RequestDigest {
		return IntentReceipt{}, ErrIntentIntegrity
	}
	if _, err := a.openIntent(stored); err != nil {
		return IntentReceipt{}, err
	}
	return intentReceipt(stored), nil
}

func intentReceipt(in model.GenerationIntent) IntentReceipt {
	return IntentReceipt{ID: in.ID, BillingID: in.BillingID, State: in.State, OutputRecordID: in.OutputRecordID, CreatedAt: in.CreatedAt, Deadline: in.Deadline, ExpiresAt: in.ExpiresAt}
}

func (a *IntentAcceptor) Read(ctx context.Context, binding access.Binding, id string) (IntentReceipt, execv1.Invocation, error) {
	principal, err := currentPrincipal(ctx, a.resolve, binding)
	if err != nil {
		return IntentReceipt{}, execv1.Invocation{}, err
	}
	stored, err := a.admission.ReadGenerationIntent(ctx, binding.UserID, binding.TokenID, id)
	if err != nil {
		return IntentReceipt{}, execv1.Invocation{}, err
	}
	if stored.UserID != binding.UserID || stored.TokenID != binding.TokenID || stored.ID != id {
		return IntentReceipt{}, execv1.Invocation{}, ErrIntentIntegrity
	}
	if !principal.Policy.Allows(stored.Model) {
		return IntentReceipt{}, execv1.Invocation{}, access.ErrUnauthorized
	}
	in, err := a.openIntent(stored)
	if err != nil {
		return IntentReceipt{}, execv1.Invocation{}, err
	}
	return intentReceipt(stored), in, nil
}

func (a *IntentAcceptor) Cancel(ctx context.Context, binding access.Binding, id string) (IntentReceipt, error) {
	// Authenticate the encrypted receipt before invoking any financial release.
	if _, _, err := a.Read(ctx, binding, id); err != nil {
		return IntentReceipt{}, err
	}
	stored, err := a.admission.CancelGenerationIntent(ctx, binding.UserID, binding.TokenID, id)
	if err != nil {
		return IntentReceipt{}, err
	}
	if stored.ID != id || stored.UserID != binding.UserID || stored.TokenID != binding.TokenID {
		return IntentReceipt{}, ErrIntentIntegrity
	}
	return intentReceipt(stored), nil
}
