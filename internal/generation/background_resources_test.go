package generation_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func TestBackgroundResourceAuthorityDeleteCASAndTombstone(t *testing.T) {
	f := newBackgroundFixture(t)
	receipt, e := f.submit(1, "terminal-delete")
	if e != nil {
		t.Fatal(e)
	}
	if kind, e := f.s.Kind(f.ctx, receipt.ID); e != nil || kind != bg.BackgroundResource {
		t.Fatal("missing positive background authority", kind, e)
	}
	if e = f.s.Delete(f.ctx, receipt.ID, nil); !errors.Is(e, bg.ErrConflict) {
		t.Fatal("active content deleted", e)
	}
	other, e := f.st.IssueToken(f.key.UserID, &store.TokenInput{Name: "other", ModelMode: access.Allowlist, Models: []string{"m"}})
	if e != nil {
		t.Fatal(e)
	}
	otherCtx, _, ok := identity.NewTokenAuthenticator(f.st).Authenticate(context.Background(), other.Key)
	if !ok {
		t.Fatal("other current key authentication failed")
	}
	if _, e = f.s.Kind(otherCtx, receipt.ID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("resource classification leaked another key's resource", e)
	}
	if e = f.s.Delete(otherCtx, receipt.ID, nil); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("other key deleted content", e)
	}
	terminal, e := f.s.Cancel(f.ctx, receipt.ID)
	if e != nil || terminal.Stage != bg.StageTerminal {
		t.Fatal("known queued cancellation failed", e)
	}
	stale := receipt.Revision
	if e = f.s.Delete(f.ctx, receipt.ID, &stale); !errors.Is(e, bg.ErrConflict) {
		t.Fatal("stale delete bypassed revision CAS", e)
	}
	if e = f.s.Delete(f.ctx, receipt.ID, &terminal.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Read(f.ctx, receipt.ID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("deleted content read", e)
	}
	if _, e = f.s.Kind(f.ctx, receipt.ID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("deleted kind disclosed", e)
	}
	if _, e = f.submit(2, "terminal-delete"); !errors.Is(e, bg.ErrCursorUnavailable) {
		t.Fatal("deleted idempotency key revived generation", e)
	}
	var row model.BackgroundJob
	if e = f.st.DB().Where("public_id = ?", receipt.ID).First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if !row.Deleted || len(row.Ciphertext) != 0 || len(row.Snapshot) != 0 || row.ReservedBytes != 0 || row.KeyHash == "" || row.RequestHash == "" {
		t.Fatal("delete lost its non-content protection or retained ciphertext")
	}
}

func TestBackgroundClassificationPreservesForegroundOwnerAndCurrentPolicy(t *testing.T) {
	f := newBackgroundFixture(t)
	digest := fmt.Sprintf("%x", sha256.Sum256(f.opts.DigestKey))
	if e := f.st.InitializeResponseRequests(f.ctx, digest); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	receipt := model.ResponseRequest{ID: strings.Repeat("a", 64), UserID: f.key.UserID, TokenID: f.key.ID, Operation: "responses", KeyHash: strings.Repeat("b", 64), RequestHash: strings.Repeat("c", 64), DigestVersion: digest, ResponseID: fmt.Sprintf("resp_%064x", 19), Model: "m", State: model.ResponseDispatching, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	if _, _, e := f.st.BeginResponseRequest(f.ctx, receipt, model.ResponseRequestLimits{InstanceRecords: 8, OwnerRecords: 4}); e != nil {
		t.Fatal(e)
	}
	if kind, e := f.s.Kind(f.ctx, receipt.ResponseID); e != nil || kind != bg.ForegroundResource {
		t.Fatalf("foreground authority missing: %s %v", kind, e)
	}
	if e := f.s.Delete(f.ctx, receipt.ResponseID, nil); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("background owner deleted foreground resource", e)
	}
	if e := f.st.DB().Model(&model.Token{}).Where("id = ?", f.key.ID).Updates(map[string]interface{}{"model_mode": access.Allowlist, "models": "[]"}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Kind(f.ctx, receipt.ResponseID); !errors.Is(e, bg.ErrNotFound) {
		t.Fatal("stale trusted context bypassed current model policy", e)
	}
}

func TestBackgroundV13KeyNamespaceFailsClosedAndBackfillsOnlyOnMatchingReplay(t *testing.T) {
	f := newBackgroundFixture(t)
	r, e := f.submit(1, "old-key")
	if e != nil {
		t.Fatal(e)
	}
	var original model.BackgroundJob
	if e = f.st.DB().Where("public_id = ?", r.ID).First(&original).Error; e != nil {
		t.Fatal(e)
	}
	if e = f.st.DB().Model(&original).Update("namespace_hash", "").Error; e != nil {
		t.Fatal(e)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(f.opts.DigestKey))
	if e = f.st.InitializeResponseRequests(f.ctx, digest); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	front := model.ResponseRequest{ID: strings.Repeat("a", 64), UserID: f.key.UserID, TokenID: f.key.ID, Operation: "responses", KeyHash: strings.Repeat("b", 64), RequestHash: strings.Repeat("c", 64), DigestVersion: digest, ResponseID: fmt.Sprintf("resp_%064x", 3), Model: "m", State: model.ResponseDispatching, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	limits := model.ResponseRequestLimits{InstanceRecords: 8, OwnerRecords: 4}
	if _, _, e = f.st.BeginResponseRequest(f.ctx, front, limits); !errors.Is(e, model.ErrResponseConflict) {
		t.Fatal("unknown legacy key namespace was assumed unrelated", e)
	}
	if replay, e := f.submit(2, "old-key"); e != nil || replay.ID != r.ID {
		t.Fatal("matching v13 replay failed", e)
	}
	var backfilled model.BackgroundJob
	if e = f.st.DB().Where("public_id = ?", r.ID).First(&backfilled).Error; e != nil {
		t.Fatal(e)
	}
	if backfilled.NamespaceHash == "" || string(backfilled.Ciphertext) != string(original.Ciphertext) || backfilled.Revision != original.Revision {
		t.Fatal("namespace replay changed accepted input or task revision")
	}
	if _, _, e = f.st.BeginResponseRequest(f.ctx, front, limits); e != nil {
		t.Fatal("resolved unrelated foreground key remained blocked", e)
	}
}
