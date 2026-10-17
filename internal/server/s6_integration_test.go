package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/model"
	"minigateway/internal/server/api"
)

// This workflow joins the real HTTP API, temporary SQLite, publication and
// gateway execution. Provider behavior is deliberately a local HTTP fixture.
func TestS6LiveAPIManagementInferenceAndRevocation(t *testing.T) {
	f := newAccessFixture(t)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer s6-upstream-fixture-secret" && r.Header.Get("Authorization") != "Bearer s6-upstream-fixture-primary" {
			http.Error(w, "wrong published credential", 401)
			return
		}
		var input struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "s6-model" {
			http.Error(w, "invalid upstream payload", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/embeddings":
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"model":"s6-model","usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case r.URL.Path == "/v1/chat/completions" && input.Stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"s6-stream\"},\"finish_reason\":null}]}\n\n")
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
		case r.URL.Path == "/v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"s6-json"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	server := httptest.NewServer(f.engine)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	request := func(method, path, body, token string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d", method, path, res.StatusCode, want)
		}
		return data
	}
	login := func(username, password string) string {
		t.Helper()
		var out struct {
			Session string `json:"session_token"`
		}
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		if err := json.Unmarshal(request("POST", "/api/auth/login", string(body), "", 200), &out); err != nil || out.Session == "" {
			t.Fatal("login did not return a session")
		}
		return out.Session
	}
	admin := login("admin", "admin-password-123!")
	var channel struct {
		Data model.Channel `json:"data"`
	}
	channelBody := fmt.Sprintf(`{"name":"s6-http","type":"openai","base_url":%q,"models":["s6-model"],"weight":1,"api_key":"s6-upstream-fixture-primary"}`, upstream.URL)
	if err := json.Unmarshal(request("POST", "/api/admin/channels", channelBody, admin, 201), &channel); err != nil || channel.Data.ID == 0 {
		t.Fatal("channel creation failed")
	}
	credentialPath := fmt.Sprintf("/api/admin/channels/%d/credentials", channel.Data.ID)
	request("POST", credentialPath, `{"name":"s6-key","api_key":"s6-upstream-fixture-secret","weight":1}`, admin, 201)
	masked := string(request("GET", credentialPath, "", admin, 200))
	if strings.Contains(masked, "s6-upstream-fixture-secret") || strings.Contains(masked, "s6-upstream-fixture-primary") {
		t.Fatal("credential list leaked its secret")
	}
	var publication struct {
		Data api.ConfigStatus `json:"data"`
	}
	if err := json.Unmarshal(request("GET", "/api/admin/config-status", "", admin, 200), &publication); err != nil || publication.Data.Channels.DesiredRevision == 0 || publication.Data.Channels.AppliedRevision != publication.Data.Channels.DesiredRevision {
		t.Fatal("saved channel/credential were not published")
	}
	request("POST", "/api/admin/users", `{"username":"s6-user","password":"s6-user-password-123!","model_mode":"allowlist","models":["s6-model"]}`, admin, 201)
	user := login("s6-user", "s6-user-password-123!")
	var issued struct {
		Data model.Token `json:"data"`
	}
	if err := json.Unmarshal(request("POST", "/api/me/keys", `{"name":"s6-client"}`, user, 201), &issued); err != nil || issued.Data.Key == "" {
		t.Fatal("key creation failed")
	}
	key := issued.Data.Key
	for _, tc := range []struct{ path, token string }{{"/api/me/models", user}, {"/v1/models", key}} {
		if !strings.Contains(string(request("GET", tc.path, "", tc.token, 200)), "s6-model") {
			t.Fatal("model visibility disagrees with published configuration")
		}
	}
	for _, tc := range []struct{ path, body, marker string }{
		{"/v1/chat/completions", `{"model":"s6-model","messages":[{"role":"user","content":"hello"}]}`, "s6-json"},
		{"/v1/chat/completions", `{"model":"s6-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "data: [DONE]"},
		{"/v1/embeddings", `{"model":"s6-model","input":"hello"}`, `"embedding":[0.25,0.5]`},
	} {
		if !strings.Contains(string(request("POST", tc.path, tc.body, key, 200)), tc.marker) {
			t.Fatalf("missing expected output for %s", tc.path)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var logs struct {
			Data []model.RequestLog `json:"data"`
		}
		if err := json.Unmarshal(request("GET", "/api/me/logs", "", user, 200), &logs); err != nil {
			t.Fatal(err)
		}
		if len(logs.Data) == 3 {
			var total, streams int
			for _, row := range logs.Data {
				if row.TokenID != issued.Data.ID || row.Status != 200 || row.IsEstimated {
					t.Fatalf("untrusted or estimated log: %+v", row)
				}
				total += row.TotalTokens
				if row.IsStream {
					streams++
				}
			}
			if total != 8 || streams != 1 {
				t.Fatalf("usage/stream accounting differs: tokens=%d streams=%d", total, streams)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inference requests did not reach owner logs")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var usage struct {
		Data struct {
			Usage struct {
				TotalRequests int `json:"total_requests"`
			} `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request("GET", "/api/me/usage", "", user, 200), &usage); err != nil || usage.Data.Usage.TotalRequests != 3 {
		t.Fatal("owner usage did not match completed requests")
	}
	before := hits.Load()
	request("GET", "/api/admin/config-status", "", user, 403)
	request("POST", "/v1/chat/completions", `{"model":"m2","messages":[]}`, key, 403)
	request("DELETE", fmt.Sprintf("/api/me/keys/%d", issued.Data.ID), "", user, 204)
	request("GET", "/v1/models", "", key, 401)
	request("POST", "/v1/chat/completions", `{"model":"s6-model","messages":[]}`, key, 401)
	if hits.Load() != before || before != 3 {
		t.Fatalf("denied/revoked calls reached upstream: before=%d after=%d", before, hits.Load())
	}
}
