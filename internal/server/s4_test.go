package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minigateway/internal/server/api"
	"minigateway/internal/server/middleware"
)

func TestEmbeddedConsoleAssetsAndUnknownPaths(t *testing.T) {
	e := newEngine(t, middleware.AdminAuth(testSecret))
	for _, tc := range []struct{ path, content string }{
		{"/", "text/html"}, {"/ui/app.mjs", "text/javascript"}, {"/ui/api.mjs", "text/javascript"}, {"/ui/workspace.mjs", "text/javascript"}, {"/ui/app.css", "text/css"},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(method, tc.path, nil))
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), tc.content) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatalf("console asset %s %s: %d %v", method, tc.path, w.Code, w.Header())
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD included an asset body")
			}
		}
	}
	for _, path := range []string{"/api/not-real", "/v1/not-real", "/ui/not-real.js", "/ui/api.test.mjs", "/ui/workspace.test.mjs", "/preview.html", "/config.yaml"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || strings.Contains(w.Body.String(), "<!doctype") {
			t.Fatalf("unknown path leaked console/source: %s %d", path, w.Code)
		}
	}
}

func TestS4SavedPublishedCallAndOwnerUsageWorkflow(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	f.createUser(t, admin, "alice", []string{"m1", "m2"})
	f.createUser(t, admin, "bob", []string{"m1"})
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	keyID, key := f.issue(t, alice)
	for _, session := range []string{"", alice, key} {
		for _, path := range []string{"/api/admin/config-status", "/api/admin/config-status/apply"} {
			method := "GET"
			if strings.HasSuffix(path, "/apply") {
				method = "POST"
			}
			code, _, _ := f.request(t, method, path, "", session)
			if code != 401 && code != 403 {
				t.Fatal("configuration state is not administrator-only")
			}
		}
	}
	readState := func() api.ConfigStatus {
		t.Helper()
		code, out, raw := f.request(t, "GET", "/api/admin/config-status", "", admin)
		var status api.ConfigStatus
		if code != 200 || json.Unmarshal(out["data"], &status) != nil {
			t.Fatalf("config state: %d %s", code, raw)
		}
		return status
	}
	before := readState()
	channel, err := f.store.GetChannel(1)
	if err != nil {
		t.Fatal(err)
	}
	code, _, raw := f.request(t, "PUT", "/api/admin/channels/1",
		fmt.Sprintf(`{"name":"published","type":"openai","base_url":%q,"models":["m1","m2"],"weight":1,"status":"enabled"}`, channel.BaseURL), admin)
	if code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	after := readState()
	if after.Epoch != before.Epoch || after.Channels.DesiredRevision != before.Channels.DesiredRevision+1 ||
		after.Channels.AppliedRevision != after.Channels.DesiredRevision {
		t.Fatalf("publication not observable: %+v", after)
	}
	code, _, raw = f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","messages":[{"role":"user","content":"hi"}]}`, key)
	if code != 200 {
		t.Fatalf("call: %d %s", code, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, out, raw := f.request(t, "GET", "/api/me/usage", "", alice)
		if code != 200 {
			t.Fatalf("usage: %d %s", code, raw)
		}
		var data struct {
			Usage struct {
				TotalRequests int `json:"total_requests"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(out["data"], &data)
		if data.Usage.TotalRequests == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("published request never appeared in owner usage")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _, _ := f.request(t, "GET", fmt.Sprintf("/api/me/keys/%d", keyID), "", bob); code != 404 {
		t.Fatal("Bob could read Alice's key")
	}
	code, _, raw = f.request(t, "GET", "/api/me/logs", "", bob)
	if code != 200 || strings.Contains(raw, `"origin_model":"m2"`) {
		t.Fatal("Bob saw Alice's logs")
	}
	// An invalid saved candidate remains pending, preserving the working pool.
	code, _, raw = f.request(t, "PUT", "/api/admin/channels/1",
		`{"name":"pending","type":"unregistered","base_url":"http://localhost:9911","models":["m2"],"weight":1}`, admin)
	if code != 200 {
		t.Fatalf("pending save: %d %s", code, raw)
	}
	pending := readState()
	if pending.Channels.LastError == "" || pending.Channels.AppliedRevision != after.Channels.AppliedRevision {
		t.Fatal("failed publication reported applied")
	}
	if code, _, raw = f.request(t, "POST", "/v1/chat/completions", `{"model":"m2","messages":[]}`, key); code != 200 {
		t.Fatalf("failed publication destroyed last working pool: %d %s", code, raw)
	}
}
