package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/model"
)

type usageFixture struct {
	taskFixture
	token, otherToken uint
	ctx, otherCtx     context.Context
}

func usageTestFixture(t *testing.T) usageFixture {
	t.Helper()
	f := usageFixture{taskFixture: taskTestFixture(t)}
	for _, other := range []bool{false, true} {
		owner, key := f.owner.ID, "usage-owner-key"
		if other {
			owner, key = f.other.ID, "usage-other-key"
		}
		token, e := f.st.CreateToken(&TokenInput{UserID: owner, Key: key, ModelMode: access.All})
		if e != nil {
			t.Fatal(e)
		}
		ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: owner, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
		if other {
			f.otherToken = token.ID
			f.otherCtx = ctx
		} else {
			f.token = token.ID
			f.ctx = ctx
		}
	}
	return f
}

func usageStart(n int) history.Start {
	return history.Start{ID: fmt.Sprintf("usage_%032x", n), RequestID: "client-correlation-only", Model: "public-model", Operation: v1.Responses, MaxAttempts: 2, QuotationVersion: strings.Repeat("a", 64)}
}
func usageTarget(id string, n int) history.Target {
	return history.Target{ID: id, Number: n, ChannelID: uint(n), CredentialID: uint(n + 100), ActualModel: fmt.Sprintf("actual-%d", n), Group: fmt.Sprintf("group-%d", n), Protocol: "openai-responses"}
}
func usageLimits() model.UsageHistoryLimits {
	return model.UsageHistoryLimits{InstanceRecords: 20, OwnerRecords: 10, RetentionSeconds: 3600}
}
func usageUnknown() v3.UsageObservation {
	return v3.UsageObservation{Completeness: v2.Unknown, Source: v2.SourceUnknown}
}
func usageN(n int64) *int64 { return &n }
func usageHost() history.HostFacts {
	return history.HostFacts{Phase: v1.ResponseReceived, Status: 200, UpstreamStatus: 200, HTTPStatus: 200, Termination: v1.Success, ProtocolComplete: true, DeliveryCommitted: true, BytesWritten: 9007199254740993}
}

func TestUsageHistoryExactUnknownZeroAndTerminalReplay(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(1)
	for range 2 {
		if e := r.Begin(f.ctx, start); e != nil {
			t.Fatal(e)
		}
	}
	if e := r.Begin(f.otherCtx, start); e != history.ErrConflict {
		t.Fatal("foreign ID collision", e)
	}
	for n := 1; n <= 2; n++ {
		target := usageTarget(start.ID, n)
		for range 2 {
			if e := r.Prepare(f.ctx, target); e != nil {
				t.Fatal(e)
			}
		}
		if e := r.MayDispatch(f.ctx, start.ID, n); e != nil {
			t.Fatal(e)
		}
		usage := usageUnknown()
		if n == 2 {
			usage = v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageN(math.MaxInt64), OutputTokens: usageN(0), TotalTokens: usageN(math.MaxInt64)}, Breakdown: v3.TokenBreakdown{CachedInputTokens: usageN(0), CacheWriteInputTokens: usageN(0), ReasoningOutputTokens: usageN(0)}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "responses_native_tokens_v2"}
		}
		receipt := history.Receipt{ID: start.ID, Number: n, Host: usageHost(), Usage: usage}
		for range 2 {
			if e := r.Observe(f.ctx, receipt); e != nil {
				t.Fatal(e)
			}
		}
		receipt.Host.BytesWritten++
		if e := r.Observe(f.ctx, receipt); e != history.ErrConflict {
			t.Fatal("conflicting receipt overwrote", e)
		}
	}
	completion := history.Completion{ID: start.ID, Host: usageHost()}
	for range 2 {
		if e := r.Finish(f.ctx, completion); e != nil {
			t.Fatal(e)
		}
	}
	completion.Interrupted = true
	if e := r.Finish(f.ctx, completion); e != history.ErrConflict {
		t.Fatal("conflicting completion", e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 3)); e != history.ErrConflict {
		t.Fatal("added after terminal", e)
	}
	if e := r.MayDispatch(f.ctx, start.ID, 1); e != history.ErrConflict {
		t.Fatal("terminal marker permitted", e)
	}
	repo := f.st.UsageHistory(false, 604800)
	admin := history.Access{Session: f.adminSession, Administrator: true}
	d, e := repo.Read(context.Background(), admin, start.ID)
	if e != nil {
		t.Fatal(e)
	}
	if d.Record.State != "sealed" || d.Record.AttemptCount != 2 || len(d.Attempts) != 2 || d.Record.QuotationVersion != start.QuotationVersion {
		t.Fatal(d)
	}
	if d.Attempts[0].Usage.InputTokens != nil || d.Attempts[0].Usage.Completeness != "unknown" || d.Attempts[1].Usage.InputTokens == nil || *d.Attempts[1].Usage.InputTokens != "9223372036854775807" || *d.Attempts[1].Usage.OutputTokens != "0" {
		t.Fatal("unknown, zero or precision lost", d)
	}
	if d.Attempts[0].Group != "group-1" || d.Attempts[1].Group != "group-2" {
		t.Fatal("retry target binding lost", d)
	}
	raw, _ := json.Marshal(d)
	if !bytes.Contains(raw, []byte(`"bytes_written":"9007199254740993"`)) {
		t.Fatal(string(raw))
	}
	owner, e := repo.Read(context.Background(), history.Access{Session: f.session}, start.ID)
	if e != nil {
		t.Fatal(e)
	}
	ownerRaw, _ := json.Marshal(owner)
	for _, secret := range []string{"actual-", "group-", "openai-responses", start.QuotationVersion, "channel_id", "credential_id"} {
		if strings.Contains(string(ownerRaw), secret) {
			t.Fatal("owner target disclosure", secret, string(ownerRaw))
		}
	}
	page, e := repo.List(context.Background(), admin, history.Query{})
	if e != nil || page.Enabled || page.RetentionSeconds != 604800 || len(page.Data) != 1 {
		t.Fatal(page, e)
	}
}

func TestUsageHistoryRefreshesAdmissionButRetainsRevokedBindingForFinalFacts(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(10)
	if e := f.st.DB().Model(&model.Token{}).Where("id = ?", f.token).Updates(map[string]any{"model_mode": access.Allowlist, "models": model.StringList{"different"}}).Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, start); e != history.ErrForbidden {
		t.Fatal("stale model permission accepted", e)
	}
	if e := f.st.DB().Model(&model.Token{}).Where("id = ?", f.token).Update("model_mode", access.All).Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, start); e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Delete(&model.Token{}, f.token).Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, usageStart(11)); e != history.ErrUnauthorized {
		t.Fatal("revoked key admitted", e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != nil {
		t.Fatal("revoked binding lost preparation", e)
	}
	if e := r.MayDispatch(f.ctx, start.ID, 1); e != nil {
		t.Fatal(e)
	}
	receipt := history.Receipt{ID: start.ID, Number: 1, Host: usageHost(), Usage: usageUnknown()}
	if e := r.Observe(f.otherCtx, receipt); e != history.ErrNotFound {
		t.Fatal("foreign binding wrote", e)
	}
	if e := r.Observe(f.ctx, receipt); e != nil {
		t.Fatal("revoked binding lost observations", e)
	}
	if e := r.Finish(f.ctx, history.Completion{ID: start.ID, Host: usageHost()}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.otherSession}, start.ID); e != history.ErrNotFound {
		t.Fatal("cross owner read", e)
	}
	if _, e := f.st.UsageHistory(true, 3600).List(context.Background(), history.Access{Session: f.session}, history.Query{OwnerID: f.other.ID}); e != history.ErrInvalid {
		t.Fatal("owner selected foreign scope", e)
	}
}

func TestUsageHistorySessionRevocationScopePaginationAndSoftDelete(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	repo := f.st.UsageHistory(true, 3600)
	for n := 20; n < 23; n++ {
		ctx := f.ctx
		if n == 22 {
			ctx = f.otherCtx
		}
		if e := r.Begin(ctx, usageStart(n)); e != nil {
			t.Fatal(e)
		}
	}
	a := history.Access{Session: f.session}
	p, e := repo.List(context.Background(), a, history.Query{Limit: 1})
	if e != nil || len(p.Data) != 1 || !p.HasMore || p.Data[0].UserID != f.owner.ID {
		t.Fatal(p, e)
	}
	p, e = repo.List(context.Background(), a, history.Query{TokenID: f.otherToken})
	if e != nil || len(p.Data) != 0 {
		t.Fatal("foreign token filter leaked", p, e)
	}
	p, e = repo.List(context.Background(), history.Access{Session: f.adminSession, Administrator: true}, history.Query{OwnerID: f.other.ID})
	if e != nil || len(p.Data) != 1 {
		t.Fatal(p, e)
	}
	if _, e = repo.Read(context.Background(), history.Access{Session: f.session, Administrator: true}, usageStart(22).ID); e != history.ErrForbidden {
		t.Fatal("owner asserted admin", e)
	}
	if e = f.st.DB().Model(&model.User{}).Where("id = ?", f.admin.ID).Update("role", model.RoleUser).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = repo.List(context.Background(), history.Access{Session: f.adminSession, Administrator: true}, history.Query{}); e != history.ErrForbidden {
		t.Fatal("demoted admin retained scope", e)
	}
	if e = f.st.DB().Delete(&model.User{}, f.owner.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Read(context.Background(), a, usageStart(20).ID); e != history.ErrUnauthorized {
		t.Fatal("soft deleted owner retained session", e)
	}
	if _, e = repo.List(context.Background(), history.Access{Session: "usage-owner-key"}, history.Query{}); e != history.ErrUnauthorized {
		t.Fatal("API key used as session", e)
	}
}

func TestUsageHistoryCapacityCleanupAndLiveExpiry(t *testing.T) {
	f := usageTestFixture(t)
	limits := model.UsageHistoryLimits{InstanceRecords: 2, OwnerRecords: 1, RetentionSeconds: 1}
	r := f.st.UsageRecorder(limits)
	a, b := usageStart(30), usageStart(31)
	if e := r.Begin(f.ctx, a); e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, b); e != history.ErrCapacity {
		t.Fatal("owner cap exceeded", e)
	}
	if e := r.Begin(f.otherCtx, b); e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.otherCtx, usageStart(32)); e != history.ErrCapacity {
		t.Fatal("instance cap exceeded", e)
	}
	if e := f.st.DB().Model(&model.UsageInvocation{}).Where("id IN ?", []string{a.ID, b.ID}).Update("expires_at", time.Now().Add(-time.Hour).UnixMilli()).Error; e != nil {
		t.Fatal(e)
	}
	if n, e := f.st.CleanupUsageHistory(context.Background(), 1000); e != nil || n != 0 {
		t.Fatal("live expiry deleted", n, e)
	}
	if _, e := f.st.UsageHistory(true, 1).Read(context.Background(), history.Access{Session: f.session}, a.ID); e != nil {
		t.Fatal("live expired record hidden", e)
	}
	if e := r.Prepare(f.ctx, usageTarget(a.ID, 1)); e != nil {
		t.Fatal(e)
	}
	if e := r.Finish(f.ctx, history.Completion{ID: a.ID, Interrupted: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.UsageHistory(true, 1).Read(context.Background(), history.Access{Session: f.session}, a.ID); e != history.ErrNotFound {
		t.Fatal("expired terminal retained query", e)
	}
	if e := r.Begin(f.ctx, usageStart(33)); e != nil {
		t.Fatal("Begin did not cleanup", e)
	}
	var count int64
	f.st.DB().Model(&model.UsageAttemptReceipt{}).Where("invocation_id = ?", a.ID).Count(&count)
	if count != 0 {
		t.Fatal("cleanup left children", count)
	}
	if e := f.st.InterruptUsageHistory(context.Background()); e != nil {
		t.Fatal(e)
	}
	if n, e := f.st.CleanupUsageHistory(context.Background(), 1); e != nil || n != 1 {
		t.Fatal("bounded cleanup", n, e)
	}
	for _, n := range []int{0, 1001} {
		if _, e := f.st.CleanupUsageHistory(context.Background(), n); e != history.ErrInvalid {
			t.Fatal("unbounded cleanup accepted", n, e)
		}
	}
}

func TestUsageHistoryCapacityRejectionCommitsBoundedExpiryCleanup(t *testing.T) {
	f := usageTestFixture(t)
	for _, row := range []any{
		&model.BillingBalance{OwnerType: "user", OwnerID: f.owner.ID, UsedUnits: 19, ReservedUnits: 71},
		&model.BillingBalance{OwnerType: "key", OwnerID: f.token, UsedUnits: 19, ReservedUnits: 71},
		&model.BillingReservation{ID: strings.Repeat("c", 32), UserID: f.owner.ID, TokenID: f.token, PriceVersion: strings.Repeat("d", 64), State: "pending", ReservedUnits: 71, ProviderHeldUnits: 23, LeaseUntil: time.Now().Add(time.Hour).UnixMilli()},
		&model.BillingPriceVersion{Version: strings.Repeat("d", 64), Payload: `{"format":1,"unchanged":"cleanup-financial-sentinel"}`},
	} {
		if e := f.st.DB().Create(row).Error; e != nil {
			t.Fatal(e)
		}
	}
	financialSnapshot := func() []byte {
		t.Helper()
		values := make(map[string][]map[string]any)
		for _, table := range []string{"billing_balances", "billing_reservations", "billing_attempts", "billing_resolutions", "billing_price_versions", "users", "tokens"} {
			var rows []map[string]any
			if e := f.st.DB().Table(table).Find(&rows).Error; e != nil {
				t.Fatal(e)
			}
			values[table] = rows
		}
		encoded, e := json.Marshal(values)
		if e != nil {
			t.Fatal(e)
		}
		return encoded
	}
	before := financialSnapshot()
	parents := make([]model.UsageInvocation, 1002)
	children := make([]model.UsageAttemptReceipt, len(parents))
	expiredAt := time.Now().Add(-time.Hour).UnixMilli()
	for i := range parents {
		owner, token := f.other.ID, f.otherToken
		if i >= 1000 {
			owner, token = f.owner.ID, f.token
		}
		id := fmt.Sprintf("usage_%032x", 100000+i)
		parents[i] = model.UsageInvocation{ID: id, UserID: owner, TokenID: token, State: "sealed", MaxAttempts: 1, AttemptCount: 1, ExpiresAt: expiredAt + int64(i)}
		children[i] = model.UsageAttemptReceipt{InvocationID: id, Number: 1, State: "observed", Completeness: "unknown", Source: "unknown"}
	}
	if e := f.st.DB().CreateInBatches(&parents, 50).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().CreateInBatches(&children, 50).Error; e != nil {
		t.Fatal(e)
	}
	r := f.st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 1002, OwnerRecords: 2, RetentionSeconds: 3600})
	start := usageStart(90000)
	if e := r.Begin(f.ctx, start); e != history.ErrCapacity {
		t.Fatal("first admission should remain rejected by owner capacity", e)
	}
	var parentCount, childCount, admitted int64
	if e := f.st.DB().Model(&model.UsageInvocation{}).Count(&parentCount).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Model(&model.UsageAttemptReceipt{}).Count(&childCount).Error; e != nil {
		t.Fatal(e)
	}
	if parentCount != 2 || childCount != 2 {
		t.Fatalf("capacity rejection rolled back bounded expiry progress: parents=%d children=%d; want 2 each", parentCount, childCount)
	}
	if e := f.st.DB().Model(&model.UsageInvocation{}).Where("id = ?", start.ID).Count(&admitted).Error; e != nil || admitted != 0 {
		t.Fatal("capacity rejection created a new invocation", admitted, e)
	}
	if !bytes.Equal(before, financialSnapshot()) {
		t.Fatal("capacity cleanup changed financial authority")
	}
	if e := r.Begin(f.ctx, start); e != nil {
		t.Fatal("next admission could not advance past expired owner records", e)
	}
	if e := f.st.DB().Model(&model.UsageInvocation{}).Count(&parentCount).Error; e != nil || parentCount != 1 {
		t.Fatal("second admission did not replace expired records", parentCount, e)
	}
	if e := f.st.DB().Model(&model.UsageAttemptReceipt{}).Count(&childCount).Error; e != nil || childCount != 0 {
		t.Fatal("second cleanup retained expired children", childCount, e)
	}
	// An insert failure still rolls its preceding cleanup back with the transaction.
	if e := f.st.DB().Create(&parents[0]).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Create(&children[0]).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_insert_after_cleanup BEFORE INSERT ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private insert failure after cleanup'); END").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, usageStart(90001)); e != history.ErrUnavailable {
		t.Fatal("insert failure must remain fixed unavailable", e)
	}
	if e := f.st.DB().Model(&model.UsageInvocation{}).Where("id = ?", parents[0].ID).Count(&parentCount).Error; e != nil || parentCount != 1 {
		t.Fatal("insert failure committed cleanup", parentCount, e)
	}
	if e := f.st.DB().Model(&model.UsageAttemptReceipt{}).Where("invocation_id = ?", parents[0].ID).Count(&childCount).Error; e != nil || childCount != 1 {
		t.Fatal("insert failure split parent/child cleanup", childCount, e)
	}
	if !bytes.Equal(before, financialSnapshot()) {
		t.Fatal("expiry retries changed financial authority")
	}
}

func TestUsageHistoryConcurrentCapacityAndPrepareConflict(t *testing.T) {
	f := usageTestFixture(t)
	limits := model.UsageHistoryLimits{InstanceRecords: 2, OwnerRecords: 2, RetentionSeconds: 3600}
	r := f.st.UsageRecorder(limits)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for n := 40; n < 52; n++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); results <- r.Begin(f.ctx, usageStart(n)) }(n)
	}
	wg.Wait()
	close(results)
	success, capacity := 0, 0
	for e := range results {
		switch e {
		case nil:
			success++
		case history.ErrCapacity:
			capacity++
		default:
			t.Fatal(e)
		}
	}
	if success != 2 || capacity != 10 {
		t.Fatal(success, capacity)
	}
	var row model.UsageInvocation
	if e := f.st.DB().First(&row).Error; e != nil {
		t.Fatal(e)
	}
	a, b := usageTarget(row.ID, 1), usageTarget(row.ID, 1)
	b.Group = "other-group"
	results = make(chan error, 2)
	for _, target := range []history.Target{a, b} {
		wg.Add(1)
		go func(in history.Target) { defer wg.Done(); results <- r.Prepare(f.ctx, in) }(target)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		switch e {
		case nil:
			success++
		case history.ErrConflict:
			conflict++
		default:
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	if e := r.Prepare(f.ctx, usageTarget(row.ID, 3)); e != history.ErrConflict {
		t.Fatal("reserved slots exceeded", e)
	}
}

func TestUsageHistoryFailureRollbackAndImmutableUnknown(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(60)
	if e := r.Begin(f.ctx, start); e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_count BEFORE UPDATE OF attempt_count ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private SQL failure sentinel'); END").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != history.ErrUnavailable {
		t.Fatal("raw db error", e)
	}
	var count int64
	f.st.DB().Model(&model.UsageAttemptReceipt{}).Count(&count)
	if count != 0 {
		t.Fatal("parent/child preparation split", count)
	}
	if e := f.st.DB().Exec("DROP TRIGGER fail_usage_count").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != nil {
		t.Fatal(e)
	}
	if e := r.Finish(f.ctx, history.Completion{ID: start.ID}); e != history.ErrConflict {
		t.Fatal("sealed missing receipt", e)
	}
	if e := r.MayDispatch(f.ctx, start.ID, 1); e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_observe BEFORE UPDATE ON usage_attempt_receipts BEGIN SELECT RAISE(ABORT, 'private SQL failure sentinel'); END").Error; e != nil {
		t.Fatal(e)
	}
	receipt := history.Receipt{ID: start.ID, Number: 1, Host: usageHost(), Usage: usageUnknown()}
	if e := r.Observe(f.ctx, receipt); e != history.ErrUnavailable {
		t.Fatal(e)
	}
	if e := f.st.DB().Exec("DROP TRIGGER fail_usage_observe").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Observe(f.ctx, receipt); e != nil {
		t.Fatal(e)
	}
	changed := receipt
	changed.Usage = v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageN(0)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "responses_native_tokens_v2"}
	if e := r.Observe(f.ctx, changed); e != history.ErrConflict {
		t.Fatal("NULL/zero fingerprint collision", e)
	}
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_seal BEFORE UPDATE OF state ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private SQL failure sentinel'); END").Error; e != nil {
		t.Fatal(e)
	}
	completion := history.Completion{ID: start.ID, Host: usageHost()}
	if e := r.Finish(f.ctx, completion); e != history.ErrUnavailable {
		t.Fatal(e)
	}
	d, e := f.st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.session}, start.ID)
	if e != nil || d.Record.State != "open" || d.Attempts[0].State != "observed" {
		t.Fatal("failed seal changed facts", d, e)
	}
	if e := f.st.DB().Exec("DROP TRIGGER fail_usage_seal").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Finish(f.ctx, completion); e != nil {
		t.Fatal(e)
	}
}

func TestUsageHistoryBeginAndDispatchFailureKeepUnsentEvidence(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(65)
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_begin BEFORE INSERT ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private admission failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, start); e != history.ErrUnavailable {
		t.Fatal("Begin exposed storage error", e)
	}
	var count int64
	if e := f.st.DB().Model(&model.UsageInvocation{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("failed Begin retained admission", count, e)
	}
	if e := f.st.DB().Exec("DROP TRIGGER fail_usage_begin").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, start); e != nil {
		t.Fatal(e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != nil {
		t.Fatal(e)
	}
	if e := f.st.DB().Exec("CREATE TRIGGER fail_usage_dispatch BEFORE UPDATE OF host_phase ON usage_invocations BEGIN SELECT RAISE(ABORT, 'private dispatch failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.MayDispatch(f.ctx, start.ID, 1); e != history.ErrUnavailable {
		t.Fatal("dispatch marker failure", e)
	}
	d, e := f.st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.session}, start.ID)
	if e != nil || d.Record.Host.Phase != v1.NotDispatched || len(d.Attempts) != 1 || d.Attempts[0].MayDispatch || d.Attempts[0].State != "prepared" || d.Attempts[0].Host.Phase != v1.NotDispatched {
		t.Fatal("dispatch transaction partially committed", d, e)
	}
	if e := f.st.DB().Exec("DROP TRIGGER fail_usage_dispatch").Error; e != nil {
		t.Fatal(e)
	}
	if e := r.MayDispatch(f.ctx, start.ID, 1); e != nil {
		t.Fatal(e)
	}
	if e := r.Observe(f.ctx, history.Receipt{ID: start.ID, Number: 1, Host: history.HostFacts{Phase: v1.Dispatching}, Usage: usageUnknown()}); e != nil {
		t.Fatal("unknown panic observation rejected", e)
	}
	if e := r.Finish(f.ctx, history.Completion{ID: start.ID, Host: history.HostFacts{Phase: v1.Dispatching}, Interrupted: true}); e != nil {
		t.Fatal(e)
	}
	d, e = f.st.UsageHistory(true, 3600).Read(context.Background(), history.Access{Session: f.session}, start.ID)
	if e != nil || d.Record.State != "interrupted" || d.Record.Host.Status != 0 || d.Record.Host.Termination != "" || d.Attempts[0].Usage.InputTokens != nil {
		t.Fatal("panic turned into successful/zero result", d, e)
	}
}

func TestUsageHistoryRestartInterruptPreservesOldDataAndNullableFacts(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	a, b := usageStart(70), usageStart(71)
	for _, start := range []history.Start{a, b} {
		if e := r.Begin(f.ctx, start); e != nil {
			t.Fatal(e)
		}
		if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != nil {
			t.Fatal(e)
		}
	}
	if e := r.MayDispatch(f.ctx, b.ID, 1); e != nil {
		t.Fatal(e)
	}
	var old model.BillingPriceVersion
	old.Version = strings.Repeat("f", 64)
	old.Payload = `{"format":1,"unchanged":"sentinel"}`
	if e := f.st.DB().Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	var databases []struct{ Name, File string }
	if e := f.st.DB().Raw("PRAGMA database_list").Scan(&databases).Error; e != nil || len(databases) == 0 || databases[0].File == "" {
		t.Fatal("database path", databases, e)
	}
	if e := f.st.Close(); e != nil {
		t.Fatal(e)
	}
	// Open rows do not make Store.Close own or wait for execution lifetimes.
	st, e := Open(config.DatabaseConfig{Path: databases[0].File})
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = st.InterruptUsageHistory(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = st.InterruptUsageHistory(context.Background()); e != nil {
		t.Fatal(e)
	}
	var parents []model.UsageInvocation
	if e = st.DB().Find(&parents).Error; e != nil {
		t.Fatal(e)
	}
	for _, p := range parents {
		if p.State != "interrupted" || p.FinishedAt == 0 {
			t.Fatal(p)
		}
	}
	var children []model.UsageAttemptReceipt
	if e = st.DB().Order("invocation_id").Find(&children).Error; e != nil {
		t.Fatal(e)
	}
	for _, child := range children {
		if child.State != "interrupted" || child.InputTokens != nil || child.FinishFingerprint != "" {
			t.Fatal("invented terminal usage", child)
		}
	}
	if children[0].MayDispatch || !children[1].MayDispatch {
		t.Fatal("lost dispatch uncertainty", children)
	}
	var restored model.BillingPriceVersion
	if e = st.DB().First(&restored, "version = ?", old.Version).Error; e != nil || restored.Payload != old.Payload || !restored.CreatedAt.Equal(old.CreatedAt) {
		t.Fatal("restart changed immutable old price", restored, e)
	}
}

func TestUsageHistoryRejectsInvalidObservationsLimitsAndContexts(t *testing.T) {
	f := usageTestFixture(t)
	r := f.st.UsageRecorder(usageLimits())
	start := usageStart(80)
	for _, limits := range []model.UsageHistoryLimits{{}, {InstanceRecords: 65537, OwnerRecords: 1, RetentionSeconds: 1}, {InstanceRecords: 1, OwnerRecords: 2, RetentionSeconds: 1}, {InstanceRecords: 1, OwnerRecords: 1, RetentionSeconds: 2592001}} {
		if e := f.st.UsageRecorder(limits).Begin(f.ctx, start); e != history.ErrInvalid {
			t.Fatal(limits, e)
		}
	}
	if e := r.Begin(context.Background(), start); e != history.ErrUnauthorized {
		t.Fatal(e)
	}
	if e := r.Begin(nil, start); e != history.ErrInvalid {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if e := r.Begin(ctx, start); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := r.Begin(f.ctx, start); e != nil {
		t.Fatal(e)
	}
	if e := r.Prepare(f.ctx, usageTarget(start.ID, 1)); e != nil {
		t.Fatal(e)
	}
	invalid := history.Receipt{ID: start.ID, Number: 1, Host: usageHost(), Usage: v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageN(1), OutputTokens: usageN(0), TotalTokens: usageN(1)}, Breakdown: v3.TokenBreakdown{CachedInputTokens: usageN(2)}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "responses_native_tokens_v2"}}
	if e := r.Observe(f.ctx, invalid); e != history.ErrInvalid {
		t.Fatal("invalid containment persisted", e)
	}
	invalid.Usage = usageUnknown()
	if e := r.Observe(f.ctx, invalid); e != history.ErrConflict {
		t.Fatal("observed dispatched result without marker", e)
	}
	if _, e := f.st.UsageHistory(true, 3600).Read(nil, history.Access{Session: f.session}, start.ID); e != history.ErrInvalid {
		t.Fatal(e)
	}
}
