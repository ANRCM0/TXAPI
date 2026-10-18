package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"minigateway/internal/protocol"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

type accessFixture struct {
	engine   *gin.Engine
	store    *store.Store
	hits     *atomic.Int64
	capacity *access.Capacity
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "access.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.BootstrapAdmin("admin", "admin-password-123!"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(&store.ChannelInput{Name: "test", Type: "openai", BaseURL: upstream.URL, Models: []string{"m1", "m2"}, APIKey: "upstream-secret"}); err != nil {
		t.Fatal(err)
	}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	limiter := middleware.NewMemoryLimiter()
	t.Cleanup(limiter.(interface{ Close() }).Close)
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(), LogRecorder: st.WriteLog, Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: capacity}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	engine := NewEngine(Deps{Gateway: gw, Cfg: &config.Config{}, Store: st, RateLimiter: limiter, AdminAuth: middleware.AdminAuth(testSecret)})
	return &accessFixture{engine: engine, store: st, hits: &hits, capacity: capacity}
}
func (f *accessFixture) request(t *testing.T, method, path, body, token string) (int, map[string]json.RawMessage, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:12345"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("X-User-Id", "9999")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	out := map[string]json.RawMessage{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}
func (f *accessFixture) login(t *testing.T, name string) string {
	t.Helper()
	password := name + "-password-123"
	if name == "admin" {
		password = "admin-password-123!"
	}
	code, out, raw := f.request(t, "POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, name, password), "")
	if code != 200 {
		t.Fatalf("login %s: %d %s", name, code, raw)
	}
	var session string
	_ = json.Unmarshal(out["session_token"], &session)
	return session
}
func (f *accessFixture) createUser(t *testing.T, admin, name string, models []string) uint {
	t.Helper()
	in := store.UserInput{Username: name, Password: name + "-password-123", ModelMode: "allowlist", Models: models}
	body, _ := json.Marshal(in)
	code, out, raw := f.request(t, "POST", "/api/admin/users", string(body), admin)
	if code != 201 {
		t.Fatalf("create %s: %d %s", name, code, raw)
	}
	var user model.User
	_ = json.Unmarshal(out["data"], &user)
	return user.ID
}
func (f *accessFixture) issue(t *testing.T, session string) (uint, string) {
	t.Helper()
	code, out, raw := f.request(t, "POST", "/api/me/keys", `{"name":"client"}`, session)
	if code != 201 {
		t.Fatalf("issue key: %d %s", code, raw)
	}
	var token model.Token
	_ = json.Unmarshal(out["data"], &token)
	if token.Key == "" {
		t.Fatal("key was not returned on creation")
	}
	return token.ID, token.Key
}
func TestS2AdministratorUserAndKeyWorkflow(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	aliceID := f.createUser(t, admin, "alice", []string{"m1", "m2"})
	bobID := f.createUser(t, admin, "bob", []string{"m1"})
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	aliceKeyID, aliceKey := f.issue(t, alice)
	_, bobKey := f.issue(t, bob)
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","user_id":9999,"messages":[]}`, aliceKey); code != 200 {
		t.Fatalf("alice model2: %d %s", code, raw)
	}
	hits := f.hits.Load()
	if code, _, _ := f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","messages":[]}`, bobKey); code != 403 || f.hits.Load() != hits {
		t.Fatalf("bob bypassed model authorization: code=%d hits=%d", code, f.hits.Load())
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if code, _, raw := f.request(t, method, fmt.Sprintf("/api/me/keys/%d", aliceKeyID), `{"model_mode":"all"}`, bob); code != 404 {
			t.Fatalf("cross-owner %s returned %d %s", method, code, raw)
		}
	}
	for _, token := range []string{bob, bobKey, admin} {
		code, _, raw := f.request(t, "GET", "/api/me/keys", "", token)
		if token == bobKey {
			if code != 401 {
				t.Fatalf("API key accepted as login session: %d", code)
			}
		} else if code != 200 || strings.Contains(raw, aliceKey) {
			t.Fatalf("key listing leaked: %d %s", code, raw)
		}
	}
	if code, _, _ := f.request(t, "GET", "/api/admin/users", "", bob); code != 403 {
		t.Fatalf("ordinary user admitted to admin API: %d", code)
	}
	if code, _, _ := f.request(t, "POST", "/api/me/keys", `{"model_mode":"allowlist","models":["m2"]}`, bob); code != 400 {
		t.Fatalf("key widened user permissions: %d", code)
	}
	if code, _, _ := f.request(t, "GET", "/v1/models", "", admin); code != 401 {
		t.Fatal("management session used for model calls")
	}
	code, _, raw := f.request(t, "GET", "/v1/models", "", bobKey)
	if code != 200 || strings.Contains(raw, `"id":"m2"`) || !strings.Contains(raw, `"id":"m1"`) {
		t.Fatalf("models disagree with call authorization: %d %s", code, raw)
	}
	// The admin's own model-calling key must obey its own restrictions.
	code, out, raw := f.request(t, "POST", "/api/me/keys", `{"model_mode":"allowlist","models":["m1"]}`, admin)
	if code != 201 {
		t.Fatal(raw)
	}
	var adminKey model.Token
	_ = json.Unmarshal(out["data"], &adminKey)
	if code, _, _ := f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","messages":[]}`, adminKey.Key); code != 403 {
		t.Fatal("administrator call key bypassed model restrictions")
	}
	// Wait for async request logging and verify trusted user_id, ignoring forged input.
	deadline := time.Now().Add(3 * time.Second)
	var logs []model.RequestLog
	for time.Now().Before(deadline) {
		logs, _ = f.store.ListLogs(store.LogQuery{OwnerOnly: true, UserID: aliceID})
		if len(logs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(logs) != 1 || logs[0].UserID != aliceID {
		t.Fatalf("trusted log owner=%+v", logs)
	}
	if code, _, raw := f.request(t, "GET", fmt.Sprintf("/api/me/logs?user_id=%d", aliceID), "", bob); code != 200 || strings.Contains(raw, fmt.Sprintf(`"user_id":%d`, aliceID)) {
		t.Fatalf("cross-owner logs exposed: %d %s", code, raw)
	}
	// Immediate disable: all of Alice's keys and old sessions fail on the next request.
	code, _, raw = f.request(t, "PUT", fmt.Sprintf("/api/admin/users/%d", aliceID), `{"username":"alice","role":"user","status":"disabled","model_mode":"all"}`, admin)
	if code != 200 {
		t.Fatal(raw)
	}
	if code, _, _ := f.request(t, "GET", "/v1/models", "", aliceKey); code != 401 {
		t.Fatalf("disabled user still called models: %d", code)
	}
	if code, _, _ := f.request(t, "GET", "/api/me/keys", "", alice); code != 401 {
		t.Fatalf("disabled session still active: %d", code)
	}
	if code, _, _ := f.request(t, "GET", "/v1/models", "", bobKey); code != 200 {
		t.Fatalf("disabling Alice affected Bob: %d owner=%d", code, bobID)
	}
	if code, _, _ := f.request(t, "POST", "/api/auth/logout", `{}`, bob); code != 204 {
		t.Fatalf("logout failed: %d", code)
	}
	if code, _, _ := f.request(t, "GET", "/api/me/keys", "", bob); code != 401 {
		t.Fatal("logged out session reused")
	}
}
func TestLoginRateLimitAndNoAmbientCookieAuth(t *testing.T) {
	f := newAccessFixture(t)
	for i := 0; i < 5; i++ {
		if code, _, _ := f.request(t, "POST", "/api/auth/login", `{"username":"unknown","password":"incorrect-password"}`, ""); code != 401 {
			t.Fatalf("bad login status=%d", code)
		}
	}
	if code, _, _ := f.request(t, "POST", "/api/auth/login", `{"username":"admin","password":"admin-password-123!"}`, ""); code != 429 {
		t.Fatalf("login not rate limited: %d", code)
	}
	r := httptest.NewRequest("GET", "/api/me/keys", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: "ambient"})
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambient cookie authenticated")
	}
}

func TestS2WildcardChannelKeepsPersonalAndCallModelListsConsistent(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	f.createUser(t, admin, "alice", []string{"m1"})
	alice := f.login(t, "alice")
	_, key := f.issue(t, alice)
	channel, err := f.store.GetChannel(1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(store.ChannelInput{Name: channel.Name, Type: channel.Type, BaseURL: channel.BaseURL, Models: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := f.request(t, "PUT", "/api/admin/channels/1", string(body), admin); code != 200 {
		t.Fatalf("wildcard configuration: %d %s", code, raw)
	}
	code, out, raw := f.request(t, "GET", "/api/me/models", "", alice)
	var models []string
	if err := json.Unmarshal(out["data"], &models); err != nil {
		t.Fatal(err)
	}
	if code != 200 || len(models) != 1 || models[0] != "m1" {
		t.Fatalf("personal model list disagrees with authorization: %d %s", code, raw)
	}
	if code, _, raw := f.request(t, "GET", "/v1/models", "", key); code != 200 || !strings.Contains(raw, `"id":"m1"`) || strings.Contains(raw, `"id":"m2"`) {
		t.Fatalf("call model list: %d %s", code, raw)
	}
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key); code != 200 {
		t.Fatalf("authorized wildcard call: %d %s", code, raw)
	}
	if code, _, _ := f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","messages":[]}`, key); code != 403 {
		t.Fatalf("wildcard broadened authorization: %d", code)
	}
}

func TestS3AuthenticatedLimitsAggregateAcrossKeysAndRefresh(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	aliceID := f.createUser(t, admin, "alice", []string{"m1"})
	f.createUser(t, admin, "bob", []string{"m1"})
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	_, key1 := f.issue(t, alice)
	_, key2 := f.issue(t, alice)
	_, key3 := f.issue(t, alice)
	_, bobKey := f.issue(t, bob)
	in := store.UserInput{Username: "alice", Role: "user", ModelMode: "allowlist", Models: []string{"m1"}, RPMLimit: 2, ConcurrencyLimit: 2}
	if _, err := f.store.UpdateUser(aliceID, &in); err != nil {
		t.Fatal(err)
	}
	view, ok := f.store.LookupToken(key1)
	if !ok || view.UserRPMLimit != 2 || view.UserConcurrencyLimit != 2 || view.KeyConcurrencyLimit != 0 {
		t.Fatalf("limits not projected from current owner: %+v", view)
	}
	for _, key := range []string{key1, key2} {
		if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key); code != 200 {
			t.Fatalf("admitted key: %d %s", code, raw)
		}
	}
	before := f.hits.Load()
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","user_id":9999,"messages":[]}`, key3); code != 429 || !strings.Contains(raw, "user request rate") {
		t.Fatalf("third Key bypassed owner's RPM: %d %s", code, raw)
	}
	if f.hits.Load() != before {
		t.Fatal("rate rejection reached upstream")
	}
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, bobKey); code != 200 {
		t.Fatalf("user isolation: %d %s", code, raw)
	}
	// Raising a user limit applies to the next request without resetting the
	// existing window. Previously rejected attempts must not consume user RPM.
	in.RPMLimit = 3
	if _, err := f.store.UpdateUser(aliceID, &in); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key3); code != 200 {
		t.Fatalf("limit change not applied: %d %s", code, raw)
	}
	if code, _, _ := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key2); code != 429 {
		t.Fatalf("raising limit reset existing window: %d", code)
	}
	if s := f.capacity.Snapshot(); s.Requests != 0 || s.Attempts != 0 || s.Users != 0 || s.Keys != 0 || s.Channels != 0 {
		t.Fatalf("API leaked concurrency: %+v", s)
	}
}

func TestS3KeyLimitsApplyWithoutBlockingOtherOwners(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	aliceID := f.createUser(t, admin, "alice", []string{"m1"})
	f.createUser(t, admin, "bob", []string{"m1"})
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	keyID, key := f.issue(t, alice)
	_, otherKey := f.issue(t, alice)
	_, bobKey := f.issue(t, bob)
	in := store.TokenInput{Name: "limited", ModelMode: "allowlist", Models: []string{"m1"}, RPMLimit: 1, ConcurrencyLimit: 1}
	if _, err := f.store.UpdateOwnedToken(aliceID, keyID, &in); err != nil {
		t.Fatal(err)
	}
	view, ok := f.store.LookupToken(key)
	if !ok || view.KeyConcurrencyLimit != 1 {
		t.Fatalf("Key limit snapshot: %+v", view)
	}
	for i, entry := range []struct {
		key  string
		want int
	}{{key, 200}, {key, 429}, {otherKey, 200}, {bobKey, 200}} {
		if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, entry.key); code != entry.want {
			t.Fatalf("call %d: want=%d got=%d %s", i, entry.want, code, raw)
		}
	}
}

func TestS3HTTPConcurrencyUsesCurrentUserAndKeyLimits(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	aliceID := f.createUser(t, admin, "alice", []string{"m1"})
	f.createUser(t, admin, "bob", []string{"m1"})
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	keyID, key1 := f.issue(t, alice)
	_, key2 := f.issue(t, alice)
	_, key3 := f.issue(t, alice)
	_, bobKey := f.issue(t, bob)
	if _, err := f.store.UpdateUser(aliceID, &store.UserInput{Username: "alice", Role: "user", ModelMode: "allowlist", Models: []string{"m1"}, ConcurrencyLimit: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateOwnedToken(aliceID, keyID, &store.TokenInput{Name: "limited", ModelMode: "allowlist", Models: []string{"m1"}, ConcurrencyLimit: 1}); err != nil {
		t.Fatal(err)
	}
	reserve := func(key string) func() {
		t.Helper()
		view, ok := f.store.LookupToken(key)
		if !ok {
			t.Fatal("missing verified key")
		}
		release, err := f.capacity.AcquireRequest(context.Background(), access.Principal{UserID: view.UserID, TokenID: view.ID, UserConcurrencyLimit: view.UserConcurrencyLimit, KeyConcurrencyLimit: view.KeyConcurrencyLimit})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		return release
	}
	first := reserve(key1)
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key1); code != 429 || !strings.Contains(raw, "key concurrency") {
		t.Fatalf("Key concurrency: %d %s", code, raw)
	}
	second := reserve(key2)
	before := f.hits.Load()
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","user_id":9999,"messages":[]}`, key3); code != 429 || !strings.Contains(raw, "user concurrency") {
		t.Fatalf("user concurrency: %d %s", code, raw)
	}
	if f.hits.Load() != before {
		t.Fatal("concurrency rejection reached upstream")
	}
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, bobKey); code != 200 {
		t.Fatalf("another user lost capacity: %d %s", code, raw)
	}
	first()
	second()
	if code, _, raw := f.request(t, "POST", "/v1/chat/completions", `{"model":"m1","messages":[]}`, key1); code != 200 {
		t.Fatalf("released capacity not reusable: %d %s", code, raw)
	}
}
