// Package secrets contains only standard-library key-file and AEAD primitives.
// Callers supply domain-separated identity metadata; it owns no app lifecycle.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var (
	ErrConfiguration = errors.New("invalid secret key configuration")
	ErrIntegrity     = errors.New("encrypted content cannot be authenticated")
)

type Ring struct {
	active string
	keys   map[string]cipher.AEAD
}

func NewRing(active string, keys map[string][]byte) (*Ring, error) {
	if len(keys) == 0 || len(keys) > 8 {
		return nil, ErrConfiguration
	}
	r := &Ring{active: active, keys: make(map[string]cipher.AEAD, len(keys))}
	for id, key := range keys {
		if len(id) < 1 || len(id) > 32 || len(key) != 32 {
			return nil, ErrConfiguration
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, ErrConfiguration
			}
		}
		block, err := aes.NewCipher(append([]byte(nil), key...))
		if err != nil {
			return nil, ErrConfiguration
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, ErrConfiguration
		}
		r.keys[id] = aead
	}
	if r.keys[active] == nil {
		return nil, ErrConfiguration
	}
	return r, nil
}

func (r *Ring) Active() string { return r.active }

func Header(data []byte) ([]byte, error) {
	if len(data) < 3 || data[0] != 1 || data[1] < 1 || data[1] > 32 || len(data) < 2+int(data[1]) {
		return nil, ErrIntegrity
	}
	return data[:2+int(data[1])], nil
}

func (r *Ring) HasHeader(header []byte) bool {
	h, err := Header(header)
	return err == nil && len(h) == len(header) && r.keys[string(h[2:])] != nil
}

func (r *Ring) Seal(plain, identity []byte) ([]byte, error) {
	key := r.keys[r.active]
	header := append([]byte{1, byte(len(r.active))}, r.active...)
	nonce := make([]byte, key.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrIntegrity
	}
	aad := append(append([]byte(nil), identity...), header...)
	return key.Seal(append(header, nonce...), nonce, plain, aad), nil
}

func (r *Ring) Open(data, identity []byte) ([]byte, error) {
	header, err := Header(data)
	if err != nil {
		return nil, err
	}
	key := r.keys[string(header[2:])]
	if key == nil || len(data) < len(header)+key.NonceSize()+key.Overhead() {
		return nil, ErrIntegrity
	}
	nonce := data[len(header) : len(header)+key.NonceSize()]
	aad := append(append([]byte(nil), identity...), header...)
	plain, err := key.Open(nil, nonce, data[len(header)+len(nonce):], aad)
	if err != nil {
		return nil, ErrIntegrity
	}
	return plain, nil
}
