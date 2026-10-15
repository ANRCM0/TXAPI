// Package access defines explicit model permissions and secret primitives. It
// has no database, HTTP framework, or upstream protocol dependencies.
package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	All       = "all"
	Allowlist = "allowlist"
)

type Policy struct {
	Mode   string   `json:"mode"`
	Models []string `json:"models"`
}

func (p Policy) Validate() error {
	if p.Mode != All && p.Mode != Allowlist {
		return errors.New("model_mode must be all or allowlist")
	}
	if p.Mode == All && len(p.Models) > 0 {
		return errors.New("all policy must not contain an allowlist")
	}
	for _, m := range p.Models {
		if strings.TrimSpace(m) == "" || m == "*" {
			return errors.New("allowlist requires explicit non-wildcard model names")
		}
	}
	return nil
}
func (p Policy) Allows(m string) bool {
	if p.Mode == All {
		return true
	}
	if p.Mode != Allowlist {
		return false
	}
	for _, v := range p.Models {
		if v == m {
			return true
		}
	}
	return false
}
func Intersect(policies ...Policy) Policy {
	out := Policy{Mode: All}
	for _, p := range policies {
		if p.Mode == All {
			continue
		}
		if p.Mode != Allowlist {
			return Policy{Mode: Allowlist, Models: []string{}}
		}
		if out.Mode == All {
			out = Policy{Mode: Allowlist, Models: append([]string{}, p.Models...)}
			continue
		}
		kept := []string{}
		for _, m := range out.Models {
			if p.Allows(m) {
				kept = append(kept, m)
			}
		}
		out.Models = kept
	}
	return out
}
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
