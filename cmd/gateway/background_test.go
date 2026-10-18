package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

type backgroundRateCounter struct{ rpm, tpm, adjustments atomic.Int64 }

func (c *backgroundRateCounter) AllowRequest(context.Context, string, int, time.Time) (bool, int) {
	c.rpm.Add(1)
	return true, 0
}
func (c *backgroundRateCounter) AllowTokens(context.Context, string, int, int, time.Time) (bool, int) {
	c.tpm.Add(1)
	return true, 0
}
func (c *backgroundRateCounter) AddTokens(context.Context, string, int, time.Time) {
	c.adjustments.Add(1)
}

type backgroundHarness struct {
	t        *testing.T
	st       *store.Store
	gw       *gateway.Gateway
	runtime  *responsesRuntime
	front    *httptest.Server
	database config.DatabaseConfig
	cfg      config.ResponsesConfig
	key      *model.Token
	rate     *backgroundRateCounter
}

func newBackgroundHarness(t *testing.T, upstream string) *backgroundHarness {
	t.Helper()
	h := &backgroundHarness{t: t, rate: &backgroundRateCounter{}}
	h.database = config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "background.db"), CredentialKeysFile: runtimeKeyFile(t, "credential", 11, 13)}
	var e error
	h.st, e = store.Open(h.database)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(h.close)
	u, e := h.st.CreateUser(&store.UserInput{Username: "background-runtime", Password: "fixture-password-123", ModelMode: access.All})
	if e != nil {
		t.Fatal(e)
	}
	h.key, e = h.st.IssueToken(u.ID, &store.TokenInput{Name: "background-key", ModelMode: access.All, RPMLimit: 100, TPMLimit: 10000})
	if e != nil {
		t.Fatal(e)
	}
	ch, e := h.st.CreateChannel(&store.ChannelInput{Name: "native", Type: "openai", BaseURL: upstream, Models: []string{"m"}, APIKey: "upstream-private-secret"})
	if e != nil {
		t.Fatal(e)
	}
	h.cfg = responsesRuntimeConfig(runtimeKeyFile(t, "content", 31, 37))
	h.cfg.Background = config.BackgroundConfig{Enabled: true, ChannelIDs: []uint{ch.ID}, RetentionSeconds: 600, MetadataSeconds: 86400, InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 32 << 20, OwnerBytes: 16 << 20, InstanceActive: 8, OwnerActive: 4}
	h.start()
	return h
}
func (h *backgroundHarness) start() {
	h.t.Helper()
	capacity, e := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 4, Channel: 2})
	if e != nil {
		h.t.Fatal(e)
	}
	h.gw, e = gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: h.st.Channels(), BillingMode: billing.ModeDisabled, Limiter: h.rate, Admission: gateway.CapacityAdmission{Capacity: capacity}, TotalTimeout: 2 * time.Second})
	if e != nil {
		h.t.Fatal(e)
	}
	h.runtime, e = startResponsesRuntime(h.cfg, h.st, h.gw)
	if e != nil {
		h.t.Fatal(e)
	}
	currentStore, handler := h.st, h.runtime.Handler()
	auth := identity.NewTokenAuthenticator(currentStore)
	h.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _, ok := auth.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
}
func (h *backgroundHarness) close() {
	h.runtime.Stop()
	if h.front != nil {
		h.front.Close()
		h.front = nil
	}
	if h.runtime != nil {
		h.runtime.Close()
		h.runtime = nil
	}
	if h.gw != nil {
		h.gw.Close()
		h.gw = nil
	}
	if h.st != nil {
		_ = h.st.Close()
		h.st = nil
	}
}
func (h *backgroundHarness) reopen() {
	h.t.Helper()
	h.close()
	var e error
	h.st, e = store.Open(h.database)
	if e != nil {
		h.t.Fatal(e)
	}
	h.start()
}

type backgroundHTTPResult struct {
	code   int
	header http.Header
	body   []byte
}

func (h *backgroundHarness) call(method, path, body, key, match string) backgroundHTTPResult {
	return h.callAs(h.key.Key, method, path, body, key, match)
}
func (h *backgroundHarness) callAs(token, method, path, body, key, match string) backgroundHTTPResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, e := http.NewRequestWithContext(ctx, method, h.front.URL+path, strings.NewReader(body))
	if e != nil {
		h.t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	response, e := h.front.Client().Do(r)
	if e != nil {
		h.t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		h.t.Fatal(e)
	}
	return backgroundHTTPResult{response.StatusCode, response.Header.Clone(), data}
}
func (r backgroundHTTPResult) id(t *testing.T) string {
	t.Helper()
	var document struct{ ID string }
	if r.code != 200 || json.Unmarshal(r.body, &document) != nil || document.ID == "" {
		t.Fatalf("missing response ID: %d %s", r.code, r.body)
	}
	return document.ID
}
func (h *backgroundHarness) waitStage(id, stage string) model.BackgroundJob {
	h.t.Helper()
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		var row model.BackgroundJob
		if e := h.st.DB().Where("public_id = ?", id).First(&row).Error; e != nil {
			h.t.Fatal(e)
		}
		if row.Stage == stage {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("background resource did not reach %s", stage)
	return model.BackgroundJob{}
}

func TestNativeBackgroundHTTPDisconnectRestartCancelAndTerminalDelete(t *testing.T) {
	var posts, polls, cancels atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-private-secret" {
			t.Error("original target credential was not used")
		}
		status := "in_progress"
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/responses":
			posts.Add(1)
			var body struct {
				Background bool
				Store      *bool
				Input      string
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Background || body.Store == nil || *body.Store || body.Input != "private prompt" {
				t.Error("native background/store/input changed")
			}
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				t.Error("observer disconnect canceled worker POST")
				return
			}
			status = "queued"
		case r.Method == "GET" && r.URL.Path == "/v1/responses/resp_provider_private":
			polls.Add(1)
		case r.Method == "POST" && r.URL.Path == "/v1/responses/resp_provider_private/cancel":
			cancels.Add(1)
			status = "cancelled"
		default:
			t.Errorf("unexpected native RPC: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_provider_private","object":"response","status":%q,"model":"m","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, status)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	created := h.call("POST", "/v1/responses", `{"model":"m","background":true,"store":false,"input":"private prompt"}`, "one-generation", "")
	id := created.id(t)
	if !strings.Contains(string(created.body), `"status":"queued"`) || created.header.Get("X-TXAPI-Background-Retention") != "temporary" || created.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong durable receipt: %s %+v", created.body, created.header)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not POST")
	}
	// The create client's context/connection is already closed by call(). The
	// independently owned native POST remains alive until this provider replies.
	close(release)
	bound := h.waitStage(id, "remote_active")
	if strings.Contains(string(bound.Ciphertext), "private prompt") || strings.Contains(string(bound.Snapshot), "resp_provider_private") || strings.Contains(string(bound.Snapshot), "upstream-private-secret") {
		t.Fatal("private input/binding stored in plaintext")
	}
	if active := h.call("DELETE", "/v1/responses/"+id, "", "", ""); active.code != 409 {
		t.Fatalf("active deletion allowed: %d %s", active.code, active.body)
	}
	other, e := h.st.IssueToken(h.key.UserID, &store.TokenInput{Name: "other-key", ModelMode: access.All})
	if e != nil {
		t.Fatal(e)
	}
	if denied := h.callAs(other.Key, "GET", "/v1/responses/"+id, "", "", ""); denied.code != 404 {
		t.Fatal("creating-key isolation bypass", denied.code)
	}
	legacy, e := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: h.st.Channels(), BillingMode: billing.ModeLegacy})
	if e != nil {
		t.Fatal(e)
	}
	if runtime, e := startResponsesRuntime(config.ResponsesConfig{}, h.st, legacy); e == nil || runtime != nil {
		t.Fatal("disabled feature bypassed pending-task billing-mode guard")
	}
	legacy.Close()
	h.reopen()
	after := h.call("GET", "/v1/responses/"+id, "", "", "")
	if after.code != 200 || strings.Contains(string(after.body), "resp_provider_private") || !strings.Contains(string(after.body), id) {
		t.Fatalf("bound restart projection: %d %s", after.code, after.body)
	}
	requested := h.call("POST", "/v1/responses/"+id+"/cancel", "", "", "")
	if requested.code != 200 {
		t.Fatalf("cancel request: %d %s", requested.code, requested.body)
	}
	h.waitStage(id, "terminal")
	terminal := h.call("GET", "/v1/responses/"+id, "", "", "")
	if terminal.code != 200 || !strings.Contains(string(terminal.body), `"status":"cancelled"`) {
		t.Fatalf("native terminal not persisted: %s", terminal.body)
	}
	if again := h.call("POST", "/v1/responses/"+id+"/cancel", "", "", ""); again.code != 200 || string(again.body) != string(terminal.body) {
		t.Fatal("terminal cancel rewrote result")
	}
	if stale := h.call("DELETE", "/v1/responses/"+id, "", "", `"0"`); stale.code != 409 {
		t.Fatal("stale terminal deletion accepted", stale.code)
	}
	if deleted := h.call("DELETE", "/v1/responses/"+id, "", "", terminal.header.Get("ETag")); deleted.code != 200 {
		t.Fatalf("terminal delete: %d %s", deleted.code, deleted.body)
	}
	if hidden := h.call("GET", "/v1/responses/"+id, "", "", ""); hidden.code != 404 {
		t.Fatal("deleted content remained readable", hidden.code)
	}
	if replay := h.call("POST", "/v1/responses", `{"model":"m","background":true,"store":false,"input":"private prompt"}`, "one-generation", ""); replay.code != 409 {
		t.Fatalf("deleted generation was revived: %d %s", replay.code, replay.body)
	}
	var tombstone model.BackgroundJob
	if e = h.st.DB().Where("public_id = ?", id).First(&tombstone).Error; e != nil {
		t.Fatal(e)
	}
	if !tombstone.Deleted || len(tombstone.Ciphertext) != 0 || len(tombstone.Snapshot) != 0 || tombstone.ReservedBytes != 0 {
		t.Fatal("delete did not clear content while preserving tombstone")
	}
	if posts.Load() != 1 || cancels.Load() != 1 || h.rate.rpm.Load() != 1 || h.rate.tpm.Load() != 1 || h.rate.adjustments.Load() != 0 {
		t.Fatalf("restart/control created another generation or touched old rate epoch: post=%d poll=%d cancel=%d rpm=%d tpm=%d adjust=%d", posts.Load(), polls.Load(), cancels.Load(), h.rate.rpm.Load(), h.rate.tpm.Load(), h.rate.adjustments.Load())
	}
}

func TestNativeBackgroundRevocationUsesBoundCleanupWithoutOldPrincipal(t *testing.T) {
	var posts, cancels atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := "in_progress"
		if r.URL.Path == "/v1/responses" {
			posts.Add(1)
		} else if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
			status = "cancelled"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_revoked","object":"response","status":%q,"output":[]}`, status)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	id := h.call("POST", "/v1/responses", `{"model":"m","background":true,"input":"x"}`, "revocation", "").id(t)
	h.waitStage(id, "remote_active")
	if e := h.st.DeleteOwnedToken(h.key.UserID, h.key.ID); e != nil {
		t.Fatal(e)
	}
	if denied := h.call("GET", "/v1/responses/"+id, "", "", ""); denied.code != 401 {
		t.Fatal("revoked key retained content access", denied.code)
	}
	row := h.waitStage(id, "terminal")
	if !row.CancelRequested || posts.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("claim-bound cleanup failed or resubmitted: cancel=%v posts=%d controls=%d", row.CancelRequested, posts.Load(), cancels.Load())
	}
}

func TestNativeBackgroundAndForegroundShareIdempotencyAuthority(t *testing.T) {
	var posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_native","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	front := h.call("POST", "/v1/responses", `{"model":"m","input":"x"}`, "foreground-first", "")
	if front.code != 200 {
		t.Fatalf("foreground regressed: %d %s", front.code, front.body)
	}
	if conflicting := h.call("POST", "/v1/responses", `{"model":"m","background":true,"input":"x"}`, "foreground-first", ""); conflicting.code != 409 {
		t.Fatalf("same endpoint key was reused across authorities: %d %s", conflicting.code, conflicting.body)
	}
	id := h.call("POST", "/v1/responses", `{"model":"m","background":true,"input":"x"}`, "background-first", "").id(t)
	h.waitStage(id, "terminal")
	if conflicting := h.call("POST", "/v1/responses", `{"model":"m","input":"x"}`, "background-first", ""); conflicting.code != 409 {
		t.Fatal("foreground bypassed background namespace", conflicting.code)
	}
	if posts.Load() != 2 {
		t.Fatal("idempotency conflict caused provider I/O", posts.Load())
	}
	for _, query := range []string{"?stream=1", "?stream=true&stream=false", "?stream=true&starting_after=-1", "?stream=true&starting_after=9223372036854775808", "?starting_after=0", "?stream=true&starting_after=00", "?stream=true&starting_after=0&starting_after=1"} {
		if invalid := h.call("GET", "/v1/responses/"+id+query, "", "", ""); invalid.code != 400 {
			t.Fatalf("invalid cursor/query accepted: %s -> %d %s", query, invalid.code, invalid.body)
		}
	}
	if reserved := h.call("GET", "/v1/responses/"+id+"?stream=true&starting_after=0", "", "", ""); reserved.code != 409 {
		t.Fatal("A03-02 pretended to serve native events", reserved.code)
	}
	if old := h.call("GET", "/v1/responses/"+front.id(t)+"?stream=false", "", "", ""); old.code != 400 {
		t.Fatal("foreground GET query behavior changed", old.code)
	}
}

func TestNativeBackgroundUnknownAcceptanceNeverRepostsAfterRestart(t *testing.T) {
	var posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","status":"queued","output":[]}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	body := `{"model":"m","background":true,"input":"x"}`
	id := h.call("POST", "/v1/responses", body, "unknown", "").id(t)
	h.waitStage(id, "uncertain")
	h.reopen()
	for _, request := range []struct{ method, path, body, key string }{
		{"POST", "/v1/responses", body, "unknown"},
		{"GET", "/v1/responses/" + id, "", ""},
		{"POST", "/v1/responses/" + id + "/cancel", "", ""},
		{"DELETE", "/v1/responses/" + id, "", ""},
	} {
		result := h.call(request.method, request.path, request.body, request.key, "")
		if result.code != 409 {
			t.Fatalf("unknown execution was misrepresented: %s %s -> %d %s", request.method, request.path, result.code, result.body)
		}
	}
	if posts.Load() != 1 {
		t.Fatal("unknown acceptance resubmitted after restart", posts.Load())
	}
}

func TestNativeBackgroundCredentialRotationCannotSwitchBoundAccount(t *testing.T) {
	var posts, controls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			posts.Add(1)
		} else {
			controls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_bound","object":"response","status":"in_progress","output":[]}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	id := h.call("POST", "/v1/responses", `{"model":"m","background":true,"input":"x"}`, "binding", "").id(t)
	h.waitStage(id, "remote_active")
	h.close()
	var e error
	h.st, e = store.Open(h.database)
	if e != nil {
		t.Fatal(e)
	}
	credential := h.st.Channels()[0].Credentials[0]
	if _, e = h.st.UpdateCredential(credential.ID, &store.CredentialInput{Name: "rotated", APIKey: "new-account-secret", Weight: 1}); e != nil {
		t.Fatal(e)
	}
	baseline := controls.Load()
	h.start()
	// Two claim attempts, including the bounded failure backoff, must not
	// issue a control RPC or re-POST under the newly configured account.
	time.Sleep(2300 * time.Millisecond)
	if posts.Load() != 1 || controls.Load() != baseline {
		t.Fatalf("bound task switched target: posts=%d controls=%d baseline=%d", posts.Load(), controls.Load(), baseline)
	}
	var row model.BackgroundJob
	if e = h.st.DB().Where("public_id = ?", id).First(&row).Error; e != nil || row.Stage != "remote_active" {
		t.Fatal("target loss pretended native completion", e, row.Stage)
	}
}

func TestNativeBackgroundShutdownJoinsControlWithoutPretendingRemoteEnded(t *testing.T) {
	var posts atomic.Int64
	inspecting := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			once.Do(func() { close(inspecting) })
			<-r.Context().Done()
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_running","object":"response","status":"in_progress","output":[]}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	id := h.call("POST", "/v1/responses", `{"model":"m","background":true,"input":"x"}`, "shutdown", "").id(t)
	h.waitStage(id, "remote_active")
	select {
	case <-inspecting:
	case <-time.After(5 * time.Second):
		t.Fatal("bound inspect did not begin")
	}
	started := time.Now()
	h.runtime.Close()
	if time.Since(started) > 3*time.Second {
		t.Fatal("Close did not cancel and join bounded control RPC")
	}
	var row model.BackgroundJob
	if e := h.st.DB().Where("public_id = ?", id).First(&row).Error; e != nil || row.Stage != "remote_active" || row.CancelRequested {
		t.Fatal("shutdown fabricated native cancellation/completion", row.Stage, e)
	}
	h.reopen()
	if result := h.call("GET", "/v1/responses/"+id, "", "", ""); result.code != 200 || !strings.Contains(string(result.body), `"status":"in_progress"`) {
		t.Fatal("shutdown lost bound recovery projection", result.code, string(result.body))
	}
	if posts.Load() != 1 {
		t.Fatal("shutdown recovery re-POSTed generation", posts.Load())
	}
}

func TestNativeBackgroundRetentionSurvivesDisabledProductAndMissingContentKeys(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_retained","object":"response","status":"in_progress","output":[]}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	id := h.call("POST", "/v1/responses", `{"model":"m","background":true,"store":false,"input":"private"}`, "retention", "").id(t)
	h.waitStage(id, "remote_active")
	h.close()
	var e error
	h.st, e = store.Open(h.database)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.st.DB().Model(&model.BackgroundJob{}).Where("public_id = ?", id).Update("content_expires_at", time.Now().Add(-time.Second).UnixMilli()).Error; e != nil {
		t.Fatal(e)
	}
	h.gw, e = gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: h.st.Channels(), BillingMode: billing.ModeDisabled})
	if e != nil {
		t.Fatal(e)
	}
	h.runtime, e = startResponsesRuntime(config.ResponsesConfig{KeysFile: "missing-disabled-content-keys.json"}, h.st, h.gw)
	if e != nil || h.runtime == nil || h.runtime.Handler() != nil {
		t.Fatal("disabled product did not retain its privacy maintenance owner", e)
	}
	var row model.BackgroundJob
	if e = h.st.DB().Where("public_id = ?", id).First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Stage != "uncertain" || len(row.Ciphertext) != 0 || len(row.Snapshot) != 0 || row.ReservedBytes != 0 {
		t.Fatal("disabled startup retained expired content or erased uncertainty")
	}
	// Prove the ongoing timer is independent as well, not just startup cleanup.
	if e = h.st.DB().Model(&model.BackgroundJob{}).Where("public_id = ?", id).Updates(map[string]interface{}{"ciphertext": []byte("expired-fixture"), "reserved_bytes": 32}).Error; e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if e = h.st.DB().Where("public_id = ?", id).First(&row).Error; e != nil {
			t.Fatal(e)
		}
		if len(row.Ciphertext) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(row.Ciphertext) != 0 || row.Stage != "uncertain" {
		t.Fatal("disabled runtime stopped fixed privacy retention")
	}
}
