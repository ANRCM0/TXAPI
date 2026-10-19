package openaiauth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

const testHost = "1c343890-4575-4f24-925e-bfce1ed81833"

var testNow = time.Unix(1800000000, 0).UTC()

func begin(t *testing.T, client string) auth.Flow {
	t.Helper()
	flow, err := New().Begin(client, testHost, "http://127.0.0.1:14551/auth/callback", testNow)
	if err != nil {
		t.Fatal(err)
	}
	return flow
}

func TestBeginBindsCurrentPublicFlow(t *testing.T) {
	p := New()
	if p.Name() != "openai" {
		t.Fatal("provider name")
	}
	flow := begin(t, "")
	u, err := url.Parse(flow.URL.Value())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	challenge := sha256.Sum256([]byte(flow.Verifier.Value()))
	for name, want := range map[string]string{
		"client_id": bootstrap, "ext_agent_host_id": "urn:uuid:" + testHost, "agent_name_hint": "TXAPI",
		"redirect_uri": flow.RedirectURI, "response_type": "code", "resource": "https://api.openai.com/v1",
		"scope":                 "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
		"code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(challenge[:]),
		"state": flow.State.Value(), "nonce": flow.Nonce.Value(),
	} {
		if q.Get(name) != want {
			t.Errorf("%s mismatch", name)
		}
	}
	if u.Scheme+"://"+u.Host+u.Path != issuer+"/api/accounts/authorize" || !flow.ExpiresAt.Equal(testNow.Add(10*time.Minute)) {
		t.Fatal("endpoint/lifetime mismatch")
	}
	for _, value := range []string{flow.State.Value(), flow.Nonce.Value(), flow.Verifier.Value()} {
		if !validRandom(value) {
			t.Fatal("not a 32-byte unpadded random value")
		}
	}
	other := begin(t, "")
	if flow.State.Value() == other.State.Value() || flow.Nonce.Value() == other.Nonce.Value() || flow.Verifier.Value() == other.Verifier.Value() {
		t.Fatal("repeated secret")
	}
	returning := begin(t, "oaiapp_saved")
	returnURL, _ := url.Parse(returning.URL.Value())
	if returnURL.Query().Has("agent_name_hint") || returnURL.Query().Get("ext_agent_host_id") != "urn:uuid:"+testHost {
		t.Fatal("returning registration metadata")
	}
	for _, redirect := range []string{"http://localhost:1455/auth/callback", "https://127.0.0.1:1455/auth/callback", "http://127.0.0.1:0/auth/callback", "http://127.0.0.1:1455/callback", "http://127.0.0.1:1455/auth/callback?x=1", "http://x@127.0.0.1:1455/auth/callback", "http://127.0.0.1:1455/auth/%63allback", "http://127.0.0.1:1455/auth/callback#x"} {
		if _, err := p.Begin("", testHost, redirect, testNow); err != auth.ErrInvalid {
			t.Errorf("accepted redirect %s", redirect)
		}
	}
	if _, err := p.Begin("", "bad-host", flow.RedirectURI, testNow); err != auth.ErrInvalid {
		t.Fatal("invalid host accepted")
	}
	if _, err := p.Begin("bad\nclient", testHost, flow.RedirectURI, testNow); err != auth.ErrInvalid {
		t.Fatal("invalid client accepted")
	}
	if _, err := p.Begin("", testHost, flow.RedirectURI, time.Time{}); err != auth.ErrInvalid {
		t.Fatal("zero clock accepted")
	}
}

func TestCallbackBindsUniqueIssuedClient(t *testing.T) {
	p := New()
	flow := begin(t, "")
	good := url.Values{"code": {"test-code"}, "state": {flow.State.Value()}, "client_id": {"oaiapp_new"}, "iss": {issuer}}
	code, err := p.Callback(flow, good.Encode())
	if err != nil || code.ClientID != "oaiapp_new" || code.Value.Value() != "test-code" {
		t.Fatal("valid new registration rejected", err)
	}
	for name, change := range map[string]func(url.Values){
		"missing client":          func(q url.Values) { q.Del("client_id") },
		"wrong wire client field": func(q url.Values) { q.Del("client_id"); q.Set("issued_client_id", "oaiapp_new") },
		"bootstrap client":        func(q url.Values) { q.Set("client_id", bootstrap) },
		"blank client":            func(q url.Values) { q.Set("client_id", "") },
		"missing state":           func(q url.Values) { q.Del("state") },
		"wrong state":             func(q url.Values) { q.Set("state", "wrong") },
		"duplicate state":         func(q url.Values) { q.Add("state", flow.State.Value()) },
		"duplicate code":          func(q url.Values) { q.Add("code", "test-code") },
		"duplicate client":        func(q url.Values) { q.Add("client_id", "oaiapp_new") },
		"duplicate unknown":       func(q url.Values) { q["unknown"] = []string{"1", "2"} },
		"empty code":              func(q url.Values) { q.Set("code", "") },
		"control code":            func(q url.Values) { q.Set("code", "secret\n") },
		"wrong issuer":            func(q url.Values) { q.Set("iss", issuer+"/") },
	} {
		t.Run(name, func(t *testing.T) {
			q, _ := url.ParseQuery(good.Encode())
			change(q)
			if _, err := p.Callback(flow, q.Encode()); err != auth.ErrCallbackRejected {
				t.Fatal("unsafe callback accepted", err)
			}
		})
	}
	for _, raw := range []string{good.Encode() + "&bad=%xx", good.Encode() + "&x=1;y=2", strings.Repeat("a", 16385)} {
		if _, err := p.Callback(flow, raw); err != auth.ErrCallbackRejected {
			t.Fatal("invalid callback encoding/size")
		}
	}
	returning := begin(t, "oaiapp_saved")
	for _, correctIssuer := range []bool{true, false} {
		q, _ := url.ParseQuery(good.Encode())
		q.Set("error", "access_denied")
		q.Set("error_description", "do-not-leak")
		if !correctIssuer {
			q.Set("iss", "https://wrong.example")
		}
		_, err := p.Callback(flow, q.Encode())
		want := auth.ErrRejected
		if !correctIssuer {
			want = auth.ErrCallbackRejected
		}
		if err != want {
			t.Fatal("OAuth rejection was not bound to state and issuer", err)
		}
	}
	for _, client := range []string{"", "oaiapp_saved", "oaiapp_other"} {
		q := url.Values{"state": {returning.State.Value()}, "code": {"code"}}
		if client != "" {
			q.Set("client_id", client)
		}
		result, err := p.Callback(returning, q.Encode())
		if client == "oaiapp_other" {
			if err != auth.ErrCallbackRejected {
				t.Fatal("rebound client")
			}
		} else if err != nil || result.ClientID != "oaiapp_saved" {
			t.Fatal("returning client not retained")
		}
	}
}

func TestRequestsKeepSecretsInBoundedForms(t *testing.T) {
	p := New()
	flow := begin(t, "")
	r, err := p.CodeRequest(flow, auth.Code{ClientID: "oaiapp_issued", Value: auth.NewSecret("a+b&=code")})
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(r.Body.Value())
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != issuer+"/api/accounts/oauth/token" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" || q.Get("code") != "a+b&=code" || q.Get("code_verifier") != flow.Verifier.Value() || q.Get("client_id") != "oaiapp_issued" || q.Get("redirect_uri") != flow.RedirectURI || q.Get("resource") != "https://api.openai.com/v1" {
		t.Fatal("incorrect code exchange")
	}
	r, err = p.RefreshRequest("oaiapp_issued", auth.NewSecret("rt+&="))
	if err != nil {
		t.Fatal(err)
	}
	q, _ = url.ParseQuery(r.Body.Value())
	if q.Get("grant_type") != "refresh_token" || q.Get("refresh_token") != "rt+&=" || q.Get("client_id") != "oaiapp_issued" {
		t.Fatal("incorrect refresh form")
	}
	if strings.Contains(fmt.Sprintf("%+v", r), "rt+") {
		t.Fatal("request debug disclosed token")
	}
	for _, value := range []string{"", "bad token", strings.Repeat("x", auth.MaxTokenBytes+1), strings.Repeat("&", auth.MaxTokenBytes)} {
		if _, err := p.RefreshRequest("oaiapp_issued", auth.NewSecret(value)); err != auth.ErrInvalid {
			t.Fatal("invalid or oversized encoded refresh")
		}
	}
	if _, err := p.RefreshRequest(bootstrap, auth.NewSecret("token")); err != auth.ErrInvalid {
		t.Fatal("bootstrap refresh")
	}
	if _, err := p.CodeRequest(flow, auth.Code{ClientID: bootstrap, Value: auth.NewSecret("code")}); err != auth.ErrInvalid {
		t.Fatal("bootstrap code exchange")
	}
	keys := p.KeysRequest()
	if keys.URL != issuer+"/.well-known/jwks.json" || keys.Method != http.MethodGet || keys.Body.Value() != "" {
		t.Fatal("JWKS target")
	}
}

func TestTokenRepliesRejectAmbiguousOrPartialMaterial(t *testing.T) {
	p := New()
	good := `{"access_token":"at","refresh_token":"rt","id_token":"id","token_type":"Bearer","expires_in":3600}`
	response := func(body string) auth.Response {
		return auth.Response{Phase: execution.ResponseReceived, Complete: true, Status: 200, Body: auth.NewSecret(body)}
	}
	tokens, err := p.ReadTokens(response(good), testNow)
	if err != nil || tokens.AccessToken.Value() != "at" || tokens.RefreshToken.Value() != "rt" || tokens.IDToken.Value() != "id" || !tokens.ExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Fatal("valid token reply rejected", err)
	}
	tokens, err = p.ReadTokens(response(`{"access_token":"new","token_type":"Bearer","expires_in":1}`), testNow)
	if err != nil || tokens.RefreshToken.Value() != "" || tokens.IDToken.Value() != "" {
		t.Fatal("codec invented retained material")
	}
	bad := []string{
		`null`, `[]`, `{}`, good + ` {}`, strings.Replace(good, `"at"`, `""`, 1), strings.Replace(good, `"at"`, `"bad\nvalue"`, 1),
		strings.Replace(good, `"Bearer"`, `"bearer"`, 1), strings.Replace(good, `3600`, `0`, 1), strings.Replace(good, `3600`, `604801`, 1), strings.Replace(good, `3600`, `1.0`, 1), strings.Replace(good, `3600`, `1e3`, 1), strings.Replace(good, `3600`, `"3600"`, 1),
		strings.Replace(good, `"rt"`, `null`, 1), strings.Replace(good, `"id"`, `""`, 1),
		strings.Replace(good, `"access_token":"at"`, `"access_token":"at","access_token":"other"`, 1),
		strings.Replace(good, `"access_token":"at"`, `"access_token":"at","access_\u0074oken":"other"`, 1),
		strings.Replace(good, `"expires_in":3600`, `"expires_in":3600,"other":{"x":1,"x":2}`, 1),
		strings.Replace(good, `"at"`, `"`+strings.Repeat("x", auth.MaxTokenBytes+1)+`"`, 1), strings.Repeat(" ", auth.MaxResponseBytes) + good,
	}
	for i, body := range bad {
		if _, err := p.ReadTokens(response(body), testNow); err != auth.ErrUnavailable {
			t.Errorf("accepted malformed case %d", i)
		}
	}
	for _, change := range []func(*auth.Response){func(r *auth.Response) { r.Phase = execution.Dispatching }, func(r *auth.Response) { r.Complete = false }, func(r *auth.Response) { r.Status = 201 }} {
		r := response(good)
		change(&r)
		if _, err := p.ReadTokens(r, testNow); err != auth.ErrUnavailable {
			t.Fatal("accepted incomplete/non200 reply")
		}
	}
	for _, code := range []string{"invalid_grant", "invalid_client", "access_denied", "server_error"} {
		r := response(`{"error":"` + code + `","error_description":"secret-never-return"}`)
		r.Status = 400
		_, err := p.ReadTokens(r, testNow)
		want := auth.ErrRejected
		if code == "server_error" {
			want = auth.ErrUnavailable
		}
		if err != want {
			t.Fatal("wrong fixed category", err)
		}
		r.Complete = false
		if _, err := p.ReadTokens(r, testNow); err != auth.ErrUnavailable {
			t.Fatal("partial rejection trusted")
		}
	}
}

func TestTokenGrantedScopesAreCanonicalAndExact(t *testing.T) {
	base := `{"access_token":"at","token_type":"Bearer","expires_in":3600`
	for _, tc := range []struct {
		extra        string
		known, valid bool
		scopes       string
	}{
		{"", false, true, ""}, {`,"Scope":"ignored"`, false, true, ""},
		{`,"scope":"openid resource.invoke openid"`, true, true, "openid resource.invoke"},
		{`,"scopes":["resource.invoke","openid","openid"]`, true, true, "openid resource.invoke"},
		{`,"scope":"openid resource.invoke","scopes":["resource.invoke","openid"]`, true, true, "openid resource.invoke"},
		{`,"scope":""`, true, true, ""}, {`,"scopes":[]`, true, true, ""},
		{`,"scope":null`, false, false, ""}, {`,"scopes":null`, false, false, ""},
		{`,"scope":["openid"]`, false, false, ""}, {`,"scopes":"openid"`, false, false, ""},
		{`,"scope":"openid\tprofile"`, false, false, ""}, {`,"scopes":["openid profile"]`, false, false, ""},
		{`,"scopes":[""]`, false, false, ""}, {`,"scope":"openid","scopes":["profile"]`, false, false, ""},
		{`,"scopes":["` + strings.Repeat("x", 129) + `"]`, false, false, ""},
		{`,"scope":"` + strings.TrimSpace(strings.Repeat("openid ", 33)) + `"`, false, false, ""},
	} {
		r := auth.Response{Phase: execution.ResponseReceived, Complete: true, Status: 200, Body: auth.NewSecret(base + tc.extra + `}`)}
		tokens, err := New().ReadTokens(r, testNow)
		if !tc.valid {
			if err != auth.ErrUnavailable {
				t.Errorf("invalid scopes accepted %s", tc.extra)
			}
			continue
		}
		if err != nil || tokens.ScopesKnown != tc.known || strings.Join(tokens.Scopes, " ") != tc.scopes {
			t.Errorf("scope projection mismatch %s: %v", tc.extra, err)
		}
	}
}
