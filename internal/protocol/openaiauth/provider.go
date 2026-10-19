// OAuth mechanics adapted from codex-proxy-rs 591f95cc18ce95eb6fc87cf46a3526c5b3ecba36,
// credential/oauth.rs and token_client.rs (Apache-2.0); current OpenAI SIWC fields.
package openaiauth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

const (
	issuer       = "https://auth.openai.com"
	bootstrap    = "dynamic_agent_client"
	maximumToken = auth.MaxTokenBytes
	maximumReply = auth.MaxResponseBytes
)

type provider struct{}

func New() auth.Provider      { return provider{} }
func (provider) Name() string { return "openai" }

func randomSecret() (auth.Secret, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return auth.Secret{}, auth.ErrUnavailable
	}
	return auth.NewSecret(base64.RawURLEncoding.EncodeToString(raw[:])), nil
}

func validRedirect(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/auth/callback" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && u.Host == "127.0.0.1:"+strconv.Itoa(port)
}

func validClient(client string) bool { return auth.ValidWireText(client, 512) }

func (provider) Begin(client, host, redirect string, now time.Time) (auth.Flow, error) {
	if client == "" {
		client = bootstrap
	}
	if !validClient(client) || !auth.ValidHostID(host) || !validRedirect(redirect) || now.IsZero() {
		return auth.Flow{}, auth.ErrInvalid
	}
	state, err := randomSecret()
	if err != nil {
		return auth.Flow{}, err
	}
	nonce, err := randomSecret()
	if err != nil {
		return auth.Flow{}, err
	}
	verifier, err := randomSecret()
	if err != nil {
		return auth.Flow{}, err
	}
	challenge := sha256.Sum256([]byte(verifier.Value()))
	query := url.Values{
		"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
		"scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "resource": {"https://api.openai.com/v1"},
		"ext_agent_host_id": {"urn:uuid:" + host},
		"state":             {state.Value()}, "nonce": {nonce.Value()}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	if client == bootstrap {
		query.Set("agent_name_hint", "TXAPI")
	}
	return auth.Flow{ClientID: client, HostID: host, RedirectURI: redirect, State: state, Nonce: nonce, Verifier: verifier, URL: auth.NewSecret(issuer + "/api/accounts/authorize?" + query.Encode()), ExpiresAt: now.Add(10 * time.Minute)}, nil
}

func equalSecret(a, b string) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (provider) Callback(flow auth.Flow, raw string) (auth.Code, error) {
	if len(raw) == 0 || len(raw) > 16384 || !validClient(flow.ClientID) || !validRedirect(flow.RedirectURI) || !auth.ValidWireText(flow.State.Value(), 128) {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	for _, values := range query {
		if len(values) != 1 {
			return auth.Code{}, auth.ErrCallbackRejected
		}
	}
	if !equalSecret(query.Get("state"), flow.State.Value()) {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	if value, present := query["iss"]; present && value[0] != issuer {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	if _, present := query["error"]; present {
		return auth.Code{}, auth.ErrRejected
	}
	code := query.Get("code")
	if !auth.ValidWireText(code, 8192) {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	client := flow.ClientID
	if issued, present := query["client_id"]; present {
		if !validClient(issued[0]) || issued[0] == bootstrap || (client != bootstrap && issued[0] != client) {
			return auth.Code{}, auth.ErrCallbackRejected
		}
		client = issued[0]
	} else if client == bootstrap {
		return auth.Code{}, auth.ErrCallbackRejected
	}
	return auth.Code{Value: auth.NewSecret(code), ClientID: client}, nil
}

func formRequest(values url.Values) auth.Request {
	return auth.Request{Method: http.MethodPost, URL: issuer + "/api/accounts/oauth/token", Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}}, Body: auth.NewSecret(values.Encode())}
}

func (provider) CodeRequest(flow auth.Flow, code auth.Code) (auth.Request, error) {
	if !validRedirect(flow.RedirectURI) || !validClient(code.ClientID) || code.ClientID == bootstrap || !validClient(flow.ClientID) || (flow.ClientID != bootstrap && flow.ClientID != code.ClientID) || !auth.ValidWireText(code.Value.Value(), 8192) || !validRandom(flow.Verifier.Value()) {
		return auth.Request{}, auth.ErrInvalid
	}
	return boundedForm(url.Values{"grant_type": {"authorization_code"}, "client_id": {code.ClientID}, "redirect_uri": {flow.RedirectURI}, "resource": {"https://api.openai.com/v1"}, "code": {code.Value.Value()}, "code_verifier": {flow.Verifier.Value()}})
}

func validRandom(value string) bool {
	if len(value) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == value
}

func (provider) RefreshRequest(client string, token auth.Secret) (auth.Request, error) {
	if !validClient(client) || client == bootstrap || !auth.ValidWireText(token.Value(), maximumToken) {
		return auth.Request{}, auth.ErrInvalid
	}
	return boundedForm(url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {token.Value()}, "resource": {"https://api.openai.com/v1"}})
}

func boundedForm(values url.Values) (auth.Request, error) {
	request := formRequest(values)
	if len(request.Body.Value()) > auth.MaxRequestBytes {
		return auth.Request{}, auth.ErrInvalid
	}
	return request, nil
}

func (provider) KeysRequest() auth.Request {
	return auth.Request{Method: http.MethodGet, URL: issuer + "/.well-known/jwks.json", Header: http.Header{"Accept": {"application/json"}}}
}

// uniqueJSON rejects repeated members at every depth before typed decoding.
func uniqueJSON(data []byte, maximum int) bool {
	if len(data) == 0 || len(data) > maximum {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var scan func(int) bool
	scan = func(depth int) bool {
		if depth > 16 {
			return false
		}
		t, err := d.Token()
		if err != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !scan(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !scan(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			_, delimiter := t.(json.Delim)
			return !delimiter
		}
	}
	if !scan(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func object(data []byte, maximum int) (map[string]json.RawMessage, bool) {
	if !uniqueJSON(data, maximum) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	err := json.Unmarshal(data, &fields)
	return fields, err == nil && fields != nil
}

func stringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, exists := fields[name]
	if !exists {
		return "", false
	}
	var value string
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func integerField(fields map[string]json.RawMessage, name string) (int64, bool) {
	raw, exists := fields[name]
	if !exists {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil
}

func (provider) ReadTokens(response auth.Response, now time.Time) (auth.Tokens, error) {
	if response.Phase != execution.ResponseReceived || !response.Complete || now.IsZero() {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	fields, ok := object([]byte(response.Body.Value()), maximumReply)
	if !ok {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	if response.Status != http.StatusOK {
		code, _ := stringField(fields, "error")
		if code == "invalid_grant" || code == "invalid_client" || code == "access_denied" {
			return auth.Tokens{}, auth.ErrRejected
		}
		return auth.Tokens{}, auth.ErrUnavailable
	}
	if _, exists := fields["error"]; exists {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	access, ok := stringField(fields, "access_token")
	if !ok || !auth.ValidWireText(access, maximumToken) {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	kind, ok := stringField(fields, "token_type")
	if !ok || kind != "Bearer" {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	seconds, ok := integerField(fields, "expires_in")
	if !ok || seconds < 1 || seconds > 604800 {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	optional := make(map[string]string, 2)
	for _, name := range []string{"refresh_token", "id_token"} {
		if _, exists := fields[name]; !exists {
			continue
		}
		value, ok := stringField(fields, name)
		if !ok || !auth.ValidWireText(value, maximumToken) {
			return auth.Tokens{}, auth.ErrUnavailable
		}
		optional[name] = value
	}
	scopes, known, valid := readScopes(fields)
	if !valid {
		return auth.Tokens{}, auth.ErrUnavailable
	}
	return auth.Tokens{AccessToken: auth.NewSecret(access), RefreshToken: auth.NewSecret(optional["refresh_token"]), IDToken: auth.NewSecret(optional["id_token"]), ExpiresAt: now.Add(time.Duration(seconds) * time.Second), ScopesKnown: known, Scopes: scopes}, nil
}

func readScopes(fields map[string]json.RawMessage) ([]string, bool, bool) {
	canonical := func(values []string) ([]string, bool) {
		if len(values) > 32 {
			return nil, false
		}
		seen := make(map[string]bool, len(values))
		result := make([]string, 0, len(values))
		for _, value := range values {
			if !auth.ValidWireText(value, 128) {
				return nil, false
			}
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
		sort.Strings(result)
		return result, true
	}
	var result []string
	known := false
	if _, exists := fields["scope"]; exists {
		value, ok := stringField(fields, "scope")
		if !ok || len(value) > 32*129 {
			return nil, false, false
		}
		for _, b := range []byte(value) {
			if b < 32 || b > 126 {
				return nil, false, false
			}
		}
		result, ok = canonical(strings.Fields(value))
		if !ok {
			return nil, false, false
		}
		known = true
	}
	if raw, exists := fields["scopes"]; exists {
		var values []string
		if string(raw) == "null" || json.Unmarshal(raw, &values) != nil {
			return nil, false, false
		}
		values, ok := canonical(values)
		if !ok {
			return nil, false, false
		}
		if known && !equalScopeSets(result, values) {
			return nil, false, false
		}
		result = values
		known = true
	}
	return result, known, true
}

func equalScopeSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ auth.Provider = provider{}
