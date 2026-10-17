package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func responsesRuntimeConfig(path string) config.ResponsesConfig {
	return config.ResponsesConfig{Enabled: true, KeysFile: path, RetentionSeconds: 120, ReplaySeconds: 60, MaxOutputBytes: 65536,
		InstanceRecords: 16, OwnerRecords: 8, InstanceBytes: 2 << 20, OwnerBytes: 1 << 20, ActiveRequests: 8, OwnerRequests: 2}
}

func TestResponsesRuntimeDisabledAndPlaintextStoreFailClosed(t *testing.T) {
	var disabled *responsesRuntime
	if disabled.Handler() != nil {
		t.Fatal("typed nil handler escaped")
	}
	if r, err := startResponsesRuntime(config.ResponsesConfig{}, nil, nil); err != nil || r != nil {
		t.Fatal("disabled runtime touched dependencies", err)
	}
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plain.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r, err := startResponsesRuntime(responsesRuntimeConfig("unused"), st, nil); err == nil || r != nil {
		t.Fatal("plaintext store enabled Responses")
	}
}

func TestResponsesRuntimeRestartRetainedKeysAndUnknownExecution(t *testing.T) {
	credentialKeys := runtimeKeyFile(t, "credentials", 17, 19)
	contentKeys := runtimeKeyFile(t, "content", 31, 37)
	dbConfig := config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "responses.db"), CredentialKeysFile: credentialKeys}
	st, err := store.Open(dbConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	user, err := st.CreateUser(&store.UserInput{Username: "responses-runtime", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateChannel(&store.ChannelInput{Name: "native", Type: "openai", BaseURL: "https://fixture.invalid", Models: []string{"m"}, APIKey: "fixture-upstream-only"})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	gw, err := gateway.New(gateway.Options{Channels: st.Channels(), Client: &http.Client{Transport: generationTransport(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Error("wrong native route", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"upstream_fixture","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))}, nil
	})}, TotalTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	cfg := responsesRuntimeConfig(contentKeys)
	runtime, err := startResponsesRuntime(cfg, st, gw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.Close() }()
	request := func() *httptest.ResponseRecorder {
		t.Helper()
		principal, err := st.ResolvePrincipal(context.Background(), access.Binding{UserID: user.ID, TokenID: key.ID})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hello"}`))
		r.Header.Set("Idempotency-Key", "runtime-retry")
		r = r.WithContext(access.WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(w, r)
		return w
	}
	first, second := request(), request()
	if first.Code != 200 || second.Code != 200 || first.Body.String() != second.Body.String() || hits.Load() != 1 {
		t.Fatal("runtime did not deduplicate", first.Code, second.Code, hits.Load())
	}
	runtime.Close()
	if err := st.DB().Model(&model.ResponseRequest{}).Where("id = ?", first.Header().Get("X-TXAPI-Generation-ID")).Updates(map[string]any{"state": model.ResponseDispatching, "output_record_id": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbConfig)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = startResponsesRuntime(cfg, st, gw)
	if err != nil {
		t.Fatal(err)
	}
	unknown := request()
	if unknown.Code != 409 || !strings.Contains(unknown.Body.String(), "idempotency_unknown") || hits.Load() != 1 {
		t.Fatal("restart re-executed an uncertain response", unknown.Code, hits.Load())
	}
	runtime.Close()
	changed := cfg
	changed.KeysFile = runtimeKeyFile(t, "content", 31, 38)
	if bad, err := startResponsesRuntime(changed, st, gw); err == nil {
		bad.Close()
		t.Fatal("changed digest namespace accepted")
	}
	changed.KeysFile = runtimeKeyFile(t, "replacement", 32, 37)
	if bad, err := startResponsesRuntime(changed, st, gw); err == nil {
		bad.Close()
		t.Fatal("retained archive key was dropped")
	}
}
