package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minigateway/internal/config"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type accountCommandOutput struct {
	bytes.Buffer
	visit func(string) error
}

func (w *accountCommandOutput) Write(p []byte) (int, error) {
	var presented struct {
		URL string `json:"authorization_url"`
	}
	if json.Unmarshal(p, &presented) == nil && presented.URL != "" {
		if err := w.visit(presented.URL); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(p)
}

func TestUpstreamAccountCommandRejectsInvalidOrPlaintextOperations(t *testing.T) {
	id := "ua_0123456789abcdef0123456789abcdef"
	for _, args := range [][]string{
		{"-upstream-account-id", id},
		{"-upstream-account", "authorize"},
		{"-upstream-account", "list", "-upstream-account-limit", "101"},
		{"-upstream-account", "refresh", "-upstream-account-id", id},
		{"-upstream-account", "disable", "-upstream-account-id", id, "-upstream-account-revision", "-1"},
	} {
		flags := flag.NewFlagSet("account", flag.ContinueOnError)
		options := upstreamAccountFlags(flags)
		if err := flags.Parse(args); err != nil || !errors.Is(options.validate(false), auth.ErrInvalid) {
			t.Fatal(args, err)
		}
	}
	options := upstreamAccountOptions{action: "list", limit: 50}
	if !errors.Is(options.validate(true), auth.ErrInvalid) {
		t.Fatal("operator commands combined with bootstrap")
	}
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plain.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var output bytes.Buffer
	client := &http.Client{Transport: generationTransport(func(*http.Request) (*http.Response, error) {
		t.Error("rejected command performed network I/O")
		return nil, io.ErrUnexpectedEOF
	})}
	err = runUpstreamAccount(context.Background(), st, upstreamAccountOptions{action: "authorize", name: "test", limit: 50}, &output, client)
	if !errors.Is(err, auth.ErrEncryptionRequired) || output.Len() != 0 {
		t.Fatal("unencrypted authorization reached presentation", err)
	}
	if err := runUpstreamAccount(context.Background(), st, options, &output, client); err != nil {
		t.Fatal("metadata listing needs no authorization network", err)
	}
	var page auth.Page
	if json.Unmarshal(output.Bytes(), &page) != nil || len(page.Data) != 0 {
		t.Fatal("invalid empty metadata page")
	}
}

func TestUpstreamAccountCommandEncryptedAuthorizationRefreshAndRecovery(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "accounts.db"), CredentialKeysFile: runtimeKeyFile(t, "account-test", 34, 35)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const clientID = "oaiapp_cli_fixture"
	const access = "fixture-private-access-token"
	const refresh = "fixture-private-refresh-token"
	const rotated = "fixture-private-rotated-token"
	var nonce string
	posts, gets := 0, 0
	failRefresh := false
	jsonBody := func(value any) string {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	response := func(body string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	client := &http.Client{Transport: generationTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://auth.openai.com/.well-known/jwks.json" && request.Method == http.MethodGet {
			gets++
			return response(jsonBody(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "cli-key", "alg": "RS256", "use": "sig", "e": "AQAB", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes())}}})), nil
		}
		if request.URL.String() != "https://auth.openai.com/api/accounts/oauth/token" || request.Method != http.MethodPost {
			t.Fatal("unexpected operator network target", request.Method, request.URL.Host)
		}
		posts++
		if request.ParseForm() != nil || request.Form.Get("client_id") != clientID || request.Form.Get("resource") != "https://api.openai.com/v1" {
			t.Fatal("incorrect issued registration or token target")
		}
		if request.Form.Get("grant_type") == "refresh_token" {
			if failRefresh {
				if request.Form.Get("refresh_token") != rotated {
					t.Fatal("refresh reused the pre-rotation credential")
				}
				return nil, io.ErrUnexpectedEOF
			}
			if request.Form.Get("refresh_token") != refresh {
				t.Fatal("incorrect initial refresh binding")
			}
			return response(jsonBody(map[string]any{"access_token": access + "-new", "refresh_token": rotated, "token_type": "Bearer", "expires_in": 3600})), nil
		}
		if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "fixture-code" || len(request.Form.Get("code_verifier")) != 43 {
			t.Fatal("code/PKCE exchange was not preserved")
		}
		now := time.Now().Unix()
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"cli-key"}`))
		claims := base64.RawURLEncoding.EncodeToString([]byte(jsonBody(map[string]any{"iss": "https://auth.openai.com", "sub": "verified-cli-subject", "aud": clientID, "nonce": nonce, "iat": now, "exp": now + 3600})))
		wire := header + "." + claims
		digest := sha256.Sum256([]byte(wire))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return response(jsonBody(map[string]any{"access_token": access, "refresh_token": refresh, "id_token": wire + "." + base64.RawURLEncoding.EncodeToString(signature), "token_type": "Bearer", "expires_in": 3600, "scope": "openid profile offline_access resource.invoke chatgpt.tokens.use.direct"})), nil
	})}
	visit := func(address string) error {
		authorization, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := authorization.Query()
		nonce = query.Get("nonce")
		if authorization.Host != "auth.openai.com" || query.Get("code_challenge_method") != "S256" || nonce == "" {
			t.Fatal("invalid authorization presentation")
		}
		callback, err := url.Parse(query.Get("redirect_uri"))
		if err != nil || callback.Hostname() != "127.0.0.1" || callback.Path != "/auth/callback" {
			t.Fatal("callback was not a literal loopback listener")
		}
		callback.RawQuery = url.Values{"state": {query.Get("state")}, "code": {"fixture-code"}, "client_id": {clientID}}.Encode()
		loopback := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		defer loopback.CloseIdleConnections()
		reply, err := loopback.Get(callback.String())
		if err != nil {
			return err
		}
		defer reply.Body.Close()
		if reply.StatusCode != http.StatusOK {
			t.Fatal("bound callback rejected", reply.StatusCode)
		}
		return nil
	}
	run := func(options upstreamAccountOptions) (auth.Account, error) {
		t.Helper()
		output := &accountCommandOutput{visit: visit}
		options.limit = 50
		err := runUpstreamAccount(ctx, st, options, output, client)
		for _, secret := range []string{access, refresh, rotated, ".eyJ"} {
			if strings.Contains(output.String(), secret) {
				t.Fatal("account metadata exposed token material")
			}
		}
		if err != nil {
			return auth.Account{}, err
		}
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		if options.action == "authorize" || options.action == "reauthorize" {
			var shown map[string]string
			if decoder.Decode(&shown) != nil || shown["authorization_url"] == "" {
				t.Fatal("authorization URL was not presented before the final metadata")
			}
		}
		var account auth.Account
		if decoder.Decode(&account) != nil {
			t.Fatal("missing command metadata")
		}
		return account, nil
	}
	account, err := run(upstreamAccountOptions{action: "authorize", name: "operator fixture"})
	if err != nil || account.State != auth.Active || account.ClientID != clientID || posts != 1 || gets != 1 {
		t.Fatal("authorize did not verify and persist the registration", err, posts, gets)
	}
	forceReady := func() {
		t.Helper()
		if err := st.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", account.ID).Update("next_refresh_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
	}
	forceReady()
	account, err = run(upstreamAccountOptions{action: "refresh", id: account.ID, revision: account.Revision})
	if err != nil || account.CredentialRevision != 2 || !account.ScopesKnown || len(account.Scopes) != 5 || posts != 2 || gets != 1 {
		t.Fatal("refresh did not atomically preserve omitted identity/scopes", err, posts, gets)
	}
	forceReady()
	failRefresh = true
	_, err = run(upstreamAccountOptions{action: "refresh", id: account.ID, revision: account.Revision})
	if !errors.Is(err, auth.ErrUncertain) || posts != 3 {
		t.Fatal("ambiguous rotation was retried or treated as no send", err, posts)
	}
	account, err = st.UpstreamAccounts().Read(ctx, account.ID)
	if err != nil || account.State != auth.Uncertain {
		t.Fatal("uncertain refresh state was not durable", err)
	}
	_, err = run(upstreamAccountOptions{action: "refresh", id: account.ID, revision: account.Revision})
	if !errors.Is(err, auth.ErrUncertain) || posts != 3 {
		t.Fatal("old refresh token became reusable", err, posts)
	}
	account, err = run(upstreamAccountOptions{action: "reauthorize", id: account.ID, revision: account.Revision})
	if err != nil || account.State != auth.Active || posts != 4 || gets != 2 {
		t.Fatal("reauthorization failed after an uncertain refresh", err, posts, gets)
	}
	account, err = run(upstreamAccountOptions{action: "disable", id: account.ID, revision: account.Revision})
	if err != nil || account.State != auth.Disabled || posts != 4 || gets != 2 {
		t.Fatal("disable performed network I/O or lost fencing", err)
	}
}
