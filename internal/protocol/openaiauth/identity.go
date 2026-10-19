package openaiauth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func base64URL(value string, maximum int) ([]byte, bool) {
	if len(value) == 0 || len(value) > maximum {
		return nil, false
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	return data, err == nil && base64.RawURLEncoding.EncodeToString(data) == value
}

func publicKey(jwks []byte, kid string) (*rsa.PublicKey, bool) {
	fields, ok := object(jwks, 65536)
	if !ok {
		return nil, false
	}
	var keys []json.RawMessage
	if json.Unmarshal(fields["keys"], &keys) != nil || len(keys) == 0 || len(keys) > 32 {
		return nil, false
	}
	seen := make(map[string]bool, len(keys))
	var result *rsa.PublicKey
	for _, raw := range keys {
		key, ok := object(raw, 65536)
		if !ok {
			return nil, false
		}
		id, ok := stringField(key, "kid")
		if !ok || !auth.ValidWireText(id, 256) || seen[id] {
			return nil, false
		}
		seen[id] = true
		if id != kid {
			continue
		}
		kind, _ := stringField(key, "kty")
		if kind != "RSA" {
			return nil, false
		}
		for field, expected := range map[string]string{"use": "sig", "alg": "RS256"} {
			if _, exists := key[field]; exists {
				value, ok := stringField(key, field)
				if !ok || value != expected {
					return nil, false
				}
			}
		}
		if operations, exists := key["key_ops"]; exists {
			var ops []string
			if json.Unmarshal(operations, &ops) != nil || len(ops) != 1 || ops[0] != "verify" {
				return nil, false
			}
		}
		nText, _ := stringField(key, "n")
		eText, _ := stringField(key, "e")
		n, ok := base64URL(nText, 1366)
		if !ok || len(n) == 0 || n[0] == 0 {
			return nil, false
		}
		e, ok := base64URL(eText, 6)
		if !ok || len(e) == 0 || len(e) > 4 || e[0] == 0 {
			return nil, false
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := new(big.Int).SetBytes(e).Int64()
		if modulus.BitLen() < 2048 || modulus.BitLen() > 8192 || modulus.Bit(0) != 1 || exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
			return nil, false
		}
		result = &rsa.PublicKey{N: modulus, E: int(exponent)}
	}
	return result, result != nil
}

func audience(fields map[string]json.RawMessage, client string) bool {
	var single string
	if json.Unmarshal(fields["aud"], &single) == nil {
		return single == client && authorizedParty(fields, client, false)
	}
	var values []string
	if json.Unmarshal(fields["aud"], &values) != nil || len(values) == 0 || len(values) > 32 {
		return false
	}
	found := false
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !validClient(value) || seen[value] {
			return false
		}
		seen[value] = true
		found = found || value == client
	}
	return found && authorizedParty(fields, client, len(values) > 1)
}

func authorizedParty(fields map[string]json.RawMessage, client string, required bool) bool {
	if _, exists := fields["azp"]; exists {
		value, ok := stringField(fields, "azp")
		return ok && value == client
	}
	return !required
}

func (provider) VerifyIdentity(token, keys auth.Secret, expected auth.IdentityExpectation) (auth.Identity, error) {
	fail := func() (auth.Identity, error) { return auth.Identity{}, auth.ErrIdentityMismatch }
	if !validClient(expected.ClientID) || expected.ClientID == bootstrap || expected.Now.IsZero() || (expected.RequireNonce && !auth.ValidWireText(expected.Nonce.Value(), 128)) || !auth.ValidWireText(token.Value(), maximumToken) {
		return fail()
	}
	parts := strings.Split(token.Value(), ".")
	if len(parts) != 3 {
		return fail()
	}
	head, ok := base64URL(parts[0], 4096)
	if !ok {
		return fail()
	}
	header, ok := object(head, 4096)
	if !ok {
		return fail()
	}
	algorithm, _ := stringField(header, "alg")
	kid, _ := stringField(header, "kid")
	if algorithm != "RS256" || !auth.ValidWireText(kid, 256) {
		return fail()
	}
	if _, exists := header["crit"]; exists {
		return fail()
	}
	if _, exists := header["b64"]; exists {
		return fail()
	}
	key, ok := publicKey([]byte(keys.Value()), kid)
	if !ok {
		return fail()
	}
	signature, ok := base64URL(parts[2], 1366)
	if !ok {
		return fail()
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return fail()
	}
	// Identity claims become authoritative only after the signature succeeds.
	payload, ok := base64URL(parts[1], maximumToken)
	if !ok {
		return fail()
	}
	claims, ok := object(payload, maximumToken)
	if !ok {
		return fail()
	}
	iss, _ := stringField(claims, "iss")
	subject, _ := stringField(claims, "sub")
	if iss != issuer || !auth.ValidWireText(subject, 512) || !audience(claims, expected.ClientID) || (expected.Subject != "" && expected.Subject != subject) {
		return fail()
	}
	expires, ok := integerField(claims, "exp")
	if !ok {
		return fail()
	}
	issued, ok := integerField(claims, "iat")
	if !ok || issued < 0 || expires <= issued || issued > expected.Now.Unix()+60 || expires <= expected.Now.Unix()-60 {
		return fail()
	}
	if _, exists := claims["nbf"]; exists {
		notBefore, ok := integerField(claims, "nbf")
		if !ok || notBefore < 0 || notBefore > expected.Now.Unix()+60 || notBefore >= expires {
			return fail()
		}
	}
	if _, exists := claims["nonce"]; exists {
		nonce, ok := stringField(claims, "nonce")
		if !ok || !auth.ValidWireText(nonce, 128) || !equalSecret(nonce, expected.Nonce.Value()) {
			return fail()
		}
	} else if expected.RequireNonce {
		return fail()
	}
	if _, exists := claims["at_hash"]; exists {
		hash, ok := stringField(claims, "at_hash")
		if !ok || !auth.ValidWireText(expected.AccessToken.Value(), maximumToken) {
			return fail()
		}
		accessDigest := sha256.Sum256([]byte(expected.AccessToken.Value()))
		if !equalSecret(hash, base64.RawURLEncoding.EncodeToString(accessDigest[:16])) {
			return fail()
		}
	}
	return auth.Identity{Issuer: iss, Subject: subject}, nil
}
