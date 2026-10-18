package generation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identityv1 "minigateway/internal/foundation/contracts/identity/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/generation"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type backgroundParser struct{}
type backgroundRequest struct {
	body   []byte
	model  string
	stream bool
}

func (backgroundParser) ParseRequest(operation execv1.Operation, body []byte) (execv1.Request, error) {
	var r struct {
		Model  string
		Stream bool
	}
	if json.Unmarshal(body, &r) != nil || r.Model == "" || operation != execv1.Responses {
		return nil, errors.New("invalid background request")
	}
	return backgroundRequest{bytes.Clone(body), r.Model, r.Stream}, nil
}
func (r backgroundRequest) Operation() execv1.Operation { return execv1.Responses }
func (r backgroundRequest) Body() []byte                { return bytes.Clone(r.body) }
func (r backgroundRequest) Model() string               { return r.model }
func (r backgroundRequest) Stream() bool                { return r.stream }
func (r backgroundRequest) Requirements() execv1.Requirements {
	return execv1.Requirements{Operation: execv1.Responses, Transport: execv1.Background, Stream: r.stream}
}
func (r backgroundRequest) LegacyMaxTokens() int         { return 0 }
func (r backgroundRequest) PromptText() string           { return "private input" }
func (r backgroundRequest) AffinityText() string         { return "" }
func (r backgroundRequest) CompletionText([]byte) string { return "" }

type backgroundFixture struct {
	st   *store.Store
	s    *generation.BackgroundService
	opts generation.BackgroundOptions
	ctx  context.Context
	key  *model.Token
	path string
}

func newBackgroundFixture(t *testing.T) backgroundFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background.db")
	st, e := store.Open(config.DatabaseConfig{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	u, e := st.CreateUser(&store.UserInput{Username: "background-owner", Password: "background-password-123", ModelMode: access.Allowlist, Models: []string{"m"}})
	if e != nil {
		t.Fatal(e)
	}
	key, e := st.IssueToken(u.ID, &store.TokenInput{Name: "key", Models: []string{"m"}})
	if e != nil {
		t.Fatal(e)
	}
	ctx, _, ok := identity.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
	if !ok {
		t.Fatal("authentication failed")
	}
	opts := generation.BackgroundOptions{Repository: st, Parser: backgroundParser{}, BillingMode: "disabled", ActiveKey: "content", Keys: map[string][]byte{"content": bytes.Repeat([]byte{11}, 32)}, DigestKey: bytes.Repeat([]byte{19}, 32), Retention: 10 * time.Minute, MetadataRetention: 24 * time.Hour, Limits: bs.Limits{InstanceRecords: 8, OwnerRecords: 4, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20, InstanceActive: 4, OwnerActive: 2}}
	s, e := generation.NewBackgroundService(ctx, opts)
	if e != nil {
		t.Fatal(e)
	}
	return backgroundFixture{st, s, opts, ctx, key, path}
}
func (f backgroundFixture) submit(n int, key string) (bg.Receipt, error) {
	return f.s.Submit(f.ctx, bg.Submit{ID: fmt.Sprintf("resp_%064x", n), Operation: execv1.Responses, Body: []byte(`{"model":"m","background":true,"input":"private input"}`), IdempotencyKey: key, Store: true})
}

func TestBackgroundAcceptanceIsEncryptedIdempotentAndCurrentlyAuthorized(t *testing.T) {
	f := newBackgroundFixture(t)
	r, e := f.submit(1, "same-key")
	if e != nil || r.Stage != bg.StageQueued {
		t.Fatalf("accept: %+v %v", r, e)
	}
	replay, e := f.submit(2, "same-key")
	if e != nil || replay.ID != r.ID {
		t.Fatalf("duplicate generated another task: %+v %v", replay, e)
	}
	var rows []model.BackgroundJob
	if e := f.st.DB().Find(&rows).Error; e != nil || len(rows) != 1 {
		t.Fatalf("rows: %d %v", len(rows), e)
	}
	encoded, _ := json.Marshal(rows[0])
	if bytes.Contains(encoded, []byte("private input")) || bytes.Contains(encoded, []byte("same-key")) || bytes.Contains(encoded, []byte(f.key.Key)) {
		t.Fatal("private request or credentials persisted in plaintext")
	}
	var holds int64
	f.st.DB().Model(&model.BillingReservation{}).Count(&holds)
	if holds != 0 {
		t.Fatal("disabled background invented financial authority")
	}
	other, e := f.st.IssueToken(f.key.UserID, &store.TokenInput{Models: []string{"m"}})
	if e != nil {
		t.Fatal(e)
	}
	otherCtx, _, _ := identity.NewTokenAuthenticator(f.st).Authenticate(context.Background(), other.Key)
	if _, e = f.s.Read(otherCtx, r.ID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("another key read the task")
	}
	if _, e = f.s.Read(context.Background(), r.ID); !errors.Is(e, bg.ErrUnauthorized) {
		t.Fatal("untrusted context read the task")
	}
	if e = f.st.DeleteOwnedToken(f.key.UserID, f.key.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Read(f.ctx, r.ID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("old context survived revocation")
	}
	if _, e = f.submit(3, "new-key"); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("revoked key submitted")
	}
}

func TestBackgroundConcurrentAcceptanceQuotaAndQueuedCancellation(t *testing.T) {
	f := newBackgroundFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for n := 1; n <= 8; n++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); r, e := f.submit(n, "once"); ids <- r.ID; errs <- e }(n)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("concurrent deduplication failed")
		}
		id = got
	}
	if _, e := f.submit(20, "second"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.submit(21, "third"); !errors.Is(e, bg.ErrCapacity) {
		t.Fatalf("active cap: %v", e)
	}
	cancel, e := f.s.Cancel(f.ctx, id)
	if e != nil || !cancel.CancelRequested || cancel.Stage != bg.StageTerminal {
		t.Fatalf("queued cancel: %+v %v", cancel, e)
	}
	if _, e = f.submit(22, "third"); e != nil {
		t.Fatalf("queued cancellation retained active capacity: %v", e)
	}
	row, claim, e := f.st.ClaimBackground(f.ctx, time.Second)
	if e != nil || row.PublicID == id || claim.Token == "" {
		t.Fatalf("cancelled task claimed: %+v %v", row, e)
	}
	if _, e = f.st.UpdateBackground(f.ctx, bs.Claim{ID: claim.ID, Epoch: claim.Epoch, Token: fmt.Sprintf("%064x", 99)}, row.Revision, bs.Update{Stage: bg.StageDispatching}); !errors.Is(e, bs.ErrClaimLost) {
		t.Fatal("fake claim dispatched")
	}
}

func TestBackgroundDispatchFenceCannotReissueUncertainGeneration(t *testing.T) {
	f := newBackgroundFixture(t)
	receipt, e := f.submit(1, "claim")
	if e != nil {
		t.Fatal(e)
	}
	row, claim, e := f.st.ClaimBackground(f.ctx, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.UpdateBackground(f.ctx, claim, row.Revision, bs.Update{Stage: bg.StageDispatching}); e != nil {
		t.Fatal(e)
	}
	if e = f.st.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("claim_until", time.Now().Add(-time.Second).UnixMilli()).Error; e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.st.ClaimBackground(f.ctx, time.Second); !errors.Is(e, bg.ErrNotFound) {
		t.Fatalf("uncertain task reclaimed: %v", e)
	}
	r, e := f.s.Read(f.ctx, receipt.ID)
	if e != nil || r.Stage != bg.StageUncertain {
		t.Fatalf("expired dispatch not protected: %+v %v", r, e)
	}
	if _, e = f.st.UpdateBackground(f.ctx, claim, row.Revision+1, bs.Update{Stage: bg.StageTerminal}); !errors.Is(e, bs.ErrClaimLost) {
		t.Fatal("expired lease wrote terminal state")
	}
	if e = f.st.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("content_expires_at", time.Now().Add(-time.Second).UnixMilli()).Error; e != nil {
		t.Fatal(e)
	}
	if e = f.st.CleanupBackground(f.ctx, 8); e != nil {
		t.Fatal(e)
	}
	var stored model.BackgroundJob
	f.st.DB().First(&stored, "id = ?", row.ID)
	if len(stored.Ciphertext) != 0 || stored.Stage != string(bg.StageUncertain) {
		t.Fatal("cleanup removed uncertainty or retained content")
	}
	repeat, e := f.submit(2, "claim")
	if e != nil || repeat.ID != receipt.ID || repeat.Stage != bg.StageUncertain {
		t.Fatalf("expiry reopened unknown generation: %+v %v", repeat, e)
	}
}

func TestBackgroundStartupKeysModesAndSchemaAreFailClosed(t *testing.T) {
	f := newBackgroundFixture(t)
	r, e := f.submit(1, "restart")
	if e != nil {
		t.Fatal(e)
	}
	bad := f.opts
	bad.Keys = map[string][]byte{"content": bytes.Repeat([]byte{21}, 32)}
	if _, e = generation.NewBackgroundService(f.ctx, bad); !errors.Is(e, bg.ErrUnavailable) {
		t.Fatal("changed key accepted existing content")
	}
	bad = f.opts
	bad.DigestKey = bytes.Repeat([]byte{22}, 32)
	if _, e = generation.NewBackgroundService(f.ctx, bad); !errors.Is(e, bg.ErrUnavailable) {
		t.Fatal("changed digest created another namespace")
	}
	for _, mode := range []string{"strict", "legacy"} {
		bad = f.opts
		bad.BillingMode = mode
		if _, e = generation.NewBackgroundService(f.ctx, bad); !errors.Is(e, bg.ErrUnsupportedBilling) {
			t.Fatal("charged mode accepted")
		}
		if _, e = f.st.InitializeBackground(f.ctx, "", mode); !errors.Is(e, bg.ErrUnsupportedBilling) {
			t.Fatal("mode switch with outstanding task accepted")
		}
	}
	if e = f.st.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := store.Open(config.DatabaseConfig{Path: f.path})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f.opts.Repository = reopened
	s, e := generation.NewBackgroundService(f.ctx, f.opts)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Read(f.ctx, r.ID)
	if e != nil || got.ID != r.ID {
		t.Fatalf("restart lost admission: %+v %v", got, e)
	}
	var n int64
	if e = reopened.DB().Model(&model.SchemaMigration{}).Where("version = 13").Count(&n).Error; e != nil || n != 1 {
		t.Fatal("migration not idempotent")
	}
}

func TestBackgroundDispatchRevalidatesAndRejectsStaleRevisions(t *testing.T) {
	f := newBackgroundFixture(t)
	r, e := f.submit(1, "authorization")
	if e != nil {
		t.Fatal(e)
	}
	row, c, e := f.st.ClaimBackground(f.ctx, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Cancel(f.ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.UpdateBackground(f.ctx, c, row.Revision, bs.Update{Stage: bg.StageDispatching}); e == nil {
		t.Fatal("cancelled/stale queued claim dispatched")
	}
	_, e = f.submit(2, "revocation")
	if e != nil {
		t.Fatal(e)
	}
	row, c, e = f.st.ClaimBackground(f.ctx, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.DeleteOwnedToken(f.key.UserID, f.key.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.UpdateBackground(f.ctx, c, row.Revision, bs.Update{Stage: bg.StageDispatching}); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("dispatch survived current revocation")
	}
}

func TestBackgroundResourceIDsCannotAliasForegroundReceiptsOrHistory(t *testing.T) {
	f := newBackgroundFixture(t)
	digest := fmt.Sprintf("%x", sha256.Sum256(f.opts.DigestKey))
	if e := f.st.InitializeResponseRequests(f.ctx, digest); e != nil {
		t.Fatal(e)
	}
	newReceipt := func(n int) model.ResponseRequest {
		now := time.Now()
		return model.ResponseRequest{ID: fmt.Sprintf("%064x", n), ResponseID: fmt.Sprintf("resp_%064x", n), UserID: f.key.UserID, TokenID: f.key.ID,
			Operation: "responses", KeyHash: fmt.Sprintf("%064x", n), RequestHash: strings.Repeat("a", 64), DigestVersion: digest, Model: "m", Store: true,
			State: model.ResponseDispatching, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	}
	limits := model.ResponseRequestLimits{InstanceRecords: 8, OwnerRecords: 4}
	front := newReceipt(10)
	if _, _, e := f.st.BeginResponseRequest(f.ctx, front, limits); e != nil {
		t.Fatal(e)
	}
	if _, e := f.submit(10, "front-collision"); !errors.Is(e, bg.ErrConflict) {
		t.Fatalf("background aliased foreground receipt: %v", e)
	}
	if _, e := f.submit(11, "background"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.st.BeginResponseRequest(f.ctx, newReceipt(11), limits); !errors.Is(e, model.ErrResponseConflict) {
		t.Fatalf("foreground aliased background: %v", e)
	}
	// A duplicate uses the original resource, not a newly generated candidate ID.
	front.ResponseID = fmt.Sprintf("resp_%064x", 11)
	if got, fresh, e := f.st.BeginResponseRequest(f.ctx, front, limits); e != nil || fresh || got.ResponseID != fmt.Sprintf("resp_%064x", 10) {
		t.Fatalf("deduplication incorrectly checked unused candidate: %+v %v", got, e)
	}
	binding := identityv1.Binding{UserID: f.key.UserID, TokenID: f.key.ID}
	historyLimits := model.HistoryLimits{InstanceRecords: 8, OwnerRecords: 4, InstanceBytes: 16 << 20, OwnerBytes: 8 << 20}
	writeHistory := func(n int) error {
		now := time.Now()
		return f.st.ApplyResponseHistory(f.ctx, binding, []model.HistoryChange{{Record: model.ResponseHistory{ID: fmt.Sprintf("resp_%064x", n), UserID: binding.UserID, TokenID: binding.TokenID,
			Kind: "response", Model: "m", Version: 1, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), ReservedBytes: 64, Ciphertext: bytes.Repeat([]byte{7}, 64)}}}, nil, historyLimits)
	}
	if e := writeHistory(11); !errors.Is(e, model.ErrHistoryConflict) {
		t.Fatalf("history aliased background: %v", e)
	}
	if e := writeHistory(12); e != nil {
		t.Fatal(e)
	}
	if _, e := f.submit(12, "history-collision"); !errors.Is(e, bg.ErrConflict) {
		t.Fatalf("background aliased history: %v", e)
	}
}

func TestBackgroundMetadataCleanupIsBoundedAndProtectsUnknownWork(t *testing.T) {
	f := newBackgroundFixture(t)
	for n := 1; n <= 3; n++ {
		r, e := f.submit(n, fmt.Sprint(n))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Cancel(f.ctx, r.ID); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.st.DB().Model(&model.BackgroundJob{}).Where("stage = ?", bg.StageTerminal).Updates(map[string]interface{}{"expires_at": time.Now().Add(-time.Second).UnixMilli()}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.st.CleanupBackground(f.ctx, 1); e != nil {
		t.Fatal(e)
	}
	var n int64
	if e := f.st.DB().Model(&model.BackgroundJob{}).Count(&n).Error; e != nil || n != 2 {
		t.Fatalf("cleanup exceeded its batch bound: %d %v", n, e)
	}
}
