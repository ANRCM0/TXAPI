package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRingRetainsV1CiphertextAndBindsIdentityAndHeader(t *testing.T) {
	key := bytes.Repeat([]byte{21}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	// This is the released generation-v1 layout, independent of Ring.Seal.
	header := append([]byte{1, 5}, "key-1"...)
	nonce := bytes.Repeat([]byte{12}, aead.NonceSize())
	identity := []byte("TXAPI/generation/v1/isolated-record/1/2/3/4/65536/0/body/")
	old := aead.Seal(append(append([]byte(nil), header...), nonce...), nonce, []byte("private fixture body"), append(bytes.Clone(identity), header...))
	ring, err := NewRing("key-2", map[string][]byte{"key-1": key, "key-2": bytes.Repeat([]byte{43}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	key[0]++
	plain, err := ring.Open(old, identity)
	if err != nil || string(plain) != "private fixture body" {
		t.Fatalf("released ciphertext unreadable: %v", err)
	}
	if _, err := ring.Open(old, []byte("other owner/record/sequence")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("identity substitution accepted: %v", err)
	}
	for _, offset := range []int{0, 1, 2, len(header), len(old) - 1} {
		corrupt := bytes.Clone(old)
		corrupt[offset] ^= 1
		if _, err := ring.Open(corrupt, identity); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("tamper at %d accepted: %v", offset, err)
		}
	}
	newCipher, err := ring.Seal(plain, identity)
	if err != nil {
		t.Fatal(err)
	}
	newHeader, err := Header(newCipher)
	if err != nil || string(newHeader[2:]) != "key-2" {
		t.Fatal("rotation did not select active key")
	}
	if !ring.HasHeader(header) || ring.HasHeader([]byte{1, 2, 'x'}) {
		t.Fatal("key availability accepted malformed header")
	}
	retired, err := NewRing("key-2", map[string][]byte{"key-2": bytes.Repeat([]byte{43}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retired.Open(old, identity); !errors.Is(err, ErrIntegrity) {
		t.Fatal("missing retained key did not fail closed")
	}
}

func TestKeyFileStrictBoundedAndValueFreeErrors(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{21}, 32))
	digest := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{72}, 32))
	valid := `{"active_key":"key-1","keys":{"key-1":"` + key + `"},"digest_key":"` + digest + `"}`
	path := filepath.Join(t.TempDir(), "mounted-keys.json")
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	material, err := ReadKeyFile(path)
	if err != nil || material.Active != "key-1" || len(material.Digest) != 32 || len(material.Keys) != 1 {
		t.Fatalf("valid key file rejected: %v", err)
	}
	material.Clear()
	if !bytes.Equal(material.Digest, make([]byte, 32)) || !bytes.Equal(material.Keys["key-1"], make([]byte, 32)) {
		t.Fatal("temporary key copies not cleared")
	}
	for name, raw := range map[string]string{
		"trailing":          valid + `{}`,
		"duplicate field":   strings.Replace(valid, `"active_key":"key-1"`, `"active_key":"key-1","active_key":"key-1"`, 1),
		"escaped duplicate": strings.Replace(valid, `"active_key":"key-1"`, `"active_key":"key-1","active\u005fkey":"key-1"`, 1),
		"duplicate key":     strings.Replace(valid, `"keys":{"key-1":"`+key+`"}`, `"keys":{"key-1":"`+key+`","key-1":"`+key+`"}`, 1),
		"unknown field":     strings.Replace(valid, `"active_key"`, `"unknown"`, 1),
		"unknown active":    strings.Replace(valid, `"active_key":"key-1"`, `"active_key":"missing"`, 1),
		"null digest":       strings.Replace(valid, `"digest_key":"`+digest+`"`, `"digest_key":null`, 1),
		"same digest":       strings.Replace(valid, digest, key, 1),
		"bad key size":      strings.Replace(valid, key, "c2Vuc2l0aXZlLWtleS12YWx1ZQ==", 1),
		"invalid id":        strings.ReplaceAll(valid, "key-1", "key/1"),
		"large":             strings.Repeat(" ", 16385) + valid,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			material, err := ReadKeyFile(path)
			if !errors.Is(err, ErrConfiguration) || material.Keys != nil || material.Digest != nil {
				t.Fatalf("invalid key file accepted/retained: %v", err)
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), digest) || strings.Contains(err.Error(), path) {
				t.Fatal("secret material/path escaped generic error")
			}
		})
	}
}
