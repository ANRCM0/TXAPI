package openaiauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func signedToken(t *testing.T, key *rsa.PrivateKey, header, claims any) auth.Secret {
	t.Helper()
	return signedRawToken(t, key, jsonBytes(t, header), jsonBytes(t, claims))
}

func signedRawToken(t *testing.T, key *rsa.PrivateKey, header, claims []byte) auth.Secret {
	t.Helper()
	wire := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(wire))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewSecret(wire + "." + base64.RawURLEncoding.EncodeToString(signature))
}

func jwk(key *rsa.PrivateKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB", "alg": "RS256", "use": "sig"}
}

func TestIdentityRequiresVerifiedBinding(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	expected := auth.IdentityExpectation{ClientID: "oaiapp_issued", Subject: "subject", Nonce: auth.NewSecret("expected-nonce"), AccessToken: auth.NewSecret("access"), RequireNonce: true, Now: testNow}
	accessDigest := sha256.Sum256([]byte("access"))
	freshClaims := func() map[string]any {
		return map[string]any{"iss": issuer, "sub": "subject", "aud": "oaiapp_issued", "iat": testNow.Unix() - 30, "exp": testNow.Unix() + 600, "nonce": "expected-nonce", "at_hash": base64.RawURLEncoding.EncodeToString(accessDigest[:16])}
	}
	header := map[string]any{"alg": "RS256", "kid": "test-key"}
	keys := auth.NewSecret(string(jsonBytes(t, map[string]any{"keys": []any{jwk(key)}})))
	token := signedToken(t, key, header, freshClaims())
	identity, err := p.VerifyIdentity(token, keys, expected)
	if err != nil || identity.Issuer != issuer || identity.Subject != "subject" {
		t.Fatal("valid signature/claims rejected", err)
	}
	for name, change := range map[string]func(map[string]any){
		"wrong issuer":                  func(c map[string]any) { c["iss"] = issuer + "/" },
		"wrong audience":                func(c map[string]any) { c["aud"] = "other" },
		"missing audience":              func(c map[string]any) { delete(c, "aud") },
		"empty subject":                 func(c map[string]any) { c["sub"] = "" },
		"switched subject":              func(c map[string]any) { c["sub"] = "other" },
		"control subject":               func(c map[string]any) { c["sub"] = "bad\nsubject" },
		"expired":                       func(c map[string]any) { c["exp"] = testNow.Unix() - 61; c["iat"] = testNow.Unix() - 1000 },
		"future iat":                    func(c map[string]any) { c["iat"] = testNow.Unix() + 61 },
		"float iat":                     func(c map[string]any) { c["iat"] = json.Number("1800000000.0") },
		"string exp":                    func(c map[string]any) { c["exp"] = "1800000600" },
		"missing iat":                   func(c map[string]any) { delete(c, "iat") },
		"null exp":                      func(c map[string]any) { c["exp"] = nil },
		"future nbf":                    func(c map[string]any) { c["nbf"] = testNow.Unix() + 61 },
		"nbf after exp":                 func(c map[string]any) { c["nbf"] = testNow.Unix() + 600 },
		"float nbf":                     func(c map[string]any) { c["nbf"] = 1.5 },
		"missing nonce":                 func(c map[string]any) { delete(c, "nonce") },
		"wrong nonce":                   func(c map[string]any) { c["nonce"] = "other" },
		"null nonce":                    func(c map[string]any) { c["nonce"] = nil },
		"wrong access hash":             func(c map[string]any) { c["at_hash"] = "other" },
		"null access hash":              func(c map[string]any) { c["at_hash"] = nil },
		"multiple audience missing azp": func(c map[string]any) { c["aud"] = []string{"oaiapp_issued", "other"} },
		"wrong azp":                     func(c map[string]any) { c["azp"] = "other" },
		"duplicated audience": func(c map[string]any) {
			c["aud"] = []string{"oaiapp_issued", "oaiapp_issued"}
			c["azp"] = "oaiapp_issued"
		},
	} {
		t.Run(name, func(t *testing.T) {
			claims := freshClaims()
			change(claims)
			if _, err := p.VerifyIdentity(signedToken(t, key, header, claims), keys, expected); err != auth.ErrIdentityMismatch {
				t.Fatal("unsafe identity accepted", err)
			}
		})
	}
	claims := freshClaims()
	claims["aud"] = []string{"other", "oaiapp_issued"}
	claims["azp"] = "oaiapp_issued"
	claims["nbf"] = testNow.Unix() + 60
	if _, err := p.VerifyIdentity(signedToken(t, key, header, claims), keys, expected); err != nil {
		t.Fatal("valid multiple audience/skew rejected", err)
	}
	refresh := expected
	refresh.RequireNonce = false
	claims = freshClaims()
	delete(claims, "nonce")
	delete(claims, "at_hash")
	if _, err := p.VerifyIdentity(signedToken(t, key, header, claims), keys, refresh); err != nil {
		t.Fatal("refresh without nonce rejected", err)
	}
	claims["nonce"] = "other"
	if _, err := p.VerifyIdentity(signedToken(t, key, header, claims), keys, refresh); err != auth.ErrIdentityMismatch {
		t.Fatal("refresh nonce not checked")
	}
	for _, change := range []func(map[string]any){func(h map[string]any) { h["alg"] = "none" }, func(h map[string]any) { h["alg"] = "HS256" }, func(h map[string]any) { h["kid"] = "unknown" }, func(h map[string]any) { h["crit"] = []string{"x"} }, func(h map[string]any) { h["b64"] = false }} {
		h := map[string]any{"alg": "RS256", "kid": "test-key"}
		change(h)
		if _, err := p.VerifyIdentity(signedToken(t, key, h, freshClaims()), keys, expected); err != auth.ErrIdentityMismatch {
			t.Fatal("unsafe JOSE header")
		}
	}
	for _, value := range []auth.Secret{auth.NewSecret(token.Value() + "x"), auth.NewSecret("e30.e30."), signedRawToken(t, key, []byte(`{"alg":"RS256","alg":"RS256","kid":"test-key"}`), jsonBytes(t, freshClaims())), signedRawToken(t, key, jsonBytes(t, header), []byte(`{"iss":"https://auth.openai.com","iss":"https://auth.openai.com"}`))} {
		if _, err := p.VerifyIdentity(value, keys, expected); err != auth.ErrIdentityMismatch {
			t.Fatal("unverified/duplicate JWT accepted")
		}
	}
	for name, change := range map[string]func(map[string]any){
		"wrong key type": func(k map[string]any) { k["kty"] = "EC" }, "wrong algorithm": func(k map[string]any) { k["alg"] = "RS512" }, "encryption key": func(k map[string]any) { k["use"] = "enc" }, "signing operation": func(k map[string]any) { k["key_ops"] = []string{"sign"} },
		"small modulus": func(k map[string]any) { k["n"] = "AQAB" }, "huge modulus": func(k map[string]any) {
			k["n"] = base64.RawURLEncoding.EncodeToString(append([]byte{1}, make([]byte, 1024)...))
		},
		"even exponent": func(k map[string]any) { k["e"] = "Ag" }, "tiny exponent": func(k map[string]any) { k["e"] = "AQ" }, "huge exponent": func(k map[string]any) { k["e"] = "_____w" }, "padded exponent": func(k map[string]any) { k["e"] = "AQAB=" },
	} {
		t.Run(name, func(t *testing.T) {
			k := jwk(key)
			change(k)
			badKeys := auth.NewSecret(string(jsonBytes(t, map[string]any{"keys": []any{k}})))
			if _, err := p.VerifyIdentity(token, badKeys, expected); err != auth.ErrIdentityMismatch {
				t.Fatal("unsafe JWK accepted")
			}
		})
	}
	tooMany := make([]any, 33)
	for i := range tooMany {
		tooMany[i] = jwk(key)
	}
	for _, raw := range []string{`null`, `{"keys":[]}`, `{"keys":[null]}`, string(jsonBytes(t, map[string]any{"keys": []any{jwk(key), jwk(key)}})), string(jsonBytes(t, map[string]any{"keys": tooMany})), strings.Repeat(" ", 65537)} {
		if _, err := p.VerifyIdentity(token, auth.NewSecret(raw), expected); err != auth.ErrIdentityMismatch {
			t.Fatal("invalid/oversized JWKS accepted")
		}
	}
}
