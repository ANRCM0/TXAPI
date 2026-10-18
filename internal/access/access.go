// Package access defines explicit model permissions and secret primitives. It
// has no database, HTTP framework, or upstream protocol dependencies.
package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"

	identity "minigateway/internal/foundation/contracts/identity/v1"

	"golang.org/x/crypto/bcrypt"
)

const (
	All       = identity.All
	Allowlist = identity.Allowlist
)

type Policy = identity.Policy

func Intersect(policies ...Policy) Policy { return identity.Intersect(policies...) }
func Subset(child, parent Policy) bool {
	if child.Validate() != nil || parent.Validate() != nil {
		return false
	}
	if parent.Mode == All {
		return true
	}
	if child.Mode == All {
		return false
	}
	for _, m := range child.Models {
		if !parent.Allows(m) {
			return false
		}
	}
	return true
}

// Legacy is used only to preserve old token restrictions while hashing them.
// A legacy user_id=0 key remains unusable until explicitly claimed.
func Legacy(models []string) Policy {
	if len(models) == 0 {
		return Policy{Mode: All}
	}
	for _, m := range models {
		if m == "*" {
			return Policy{Mode: All}
		}
	}
	return Policy{Mode: Allowlist, Models: append([]string(nil), models...)}
}
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func Secret(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
func PasswordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must contain 12 to 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}
func PasswordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
func MinLimit(a, b int) int {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}
