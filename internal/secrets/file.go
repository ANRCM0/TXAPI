package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
)

type KeyMaterial struct {
	Active string
	Keys   map[string][]byte
	Digest []byte
}

// Clear drops temporary raw copies, not a secure erasure or a KMS guarantee.
func (k KeyMaterial) Clear() {
	for _, key := range k.Keys {
		clear(key)
	}
	clear(k.Digest)
}

// ReadKeyFile parses a bounded mounted secret. Every error is value-free;
// duplicate/unknown fields, trailing values and invalid key lengths fail closed.
func ReadKeyFile(path string) (material KeyMaterial, err error) {
	f, err := os.Open(path)
	if err != nil {
		return material, ErrConfiguration
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return material, ErrConfiguration
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	defer clear(raw)
	if err != nil || len(raw) > 16384 {
		return material, ErrConfiguration
	}
	defer func() {
		if err != nil {
			material.Clear()
			material = KeyMaterial{}
		}
	}()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return material, ErrConfiguration
	}
	seen := map[string]bool{}
	material.Keys = make(map[string][]byte)
	decodeSecret := func() ([]byte, error) {
		var value string
		if decoder.Decode(&value) != nil {
			return nil, ErrConfiguration
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(decoded) != 32 {
			clear(decoded)
			return nil, ErrConfiguration
		}
		return decoded, nil
	}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return material, ErrConfiguration
		}
		name, ok := field.(string)
		if !ok || seen[name] {
			return material, ErrConfiguration
		}
		seen[name] = true
		switch name {
		case "active_key":
			if decoder.Decode(&material.Active) != nil {
				return material, ErrConfiguration
			}
		case "digest_key":
			material.Digest, err = decodeSecret()
			if err != nil {
				return material, err
			}
		case "keys":
			if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
				return material, ErrConfiguration
			}
			for decoder.More() {
				field, err := decoder.Token()
				if err != nil {
					return material, ErrConfiguration
				}
				id, ok := field.(string)
				if !ok || material.Keys[id] != nil || len(material.Keys) >= 8 {
					return material, ErrConfiguration
				}
				key, err := decodeSecret()
				if err != nil {
					return material, err
				}
				material.Keys[id] = key
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
				return material, ErrConfiguration
			}
		default:
			return material, ErrConfiguration
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return material, ErrConfiguration
	}
	if _, err := decoder.Token(); err != io.EOF {
		return material, ErrConfiguration
	}
	if _, err := NewRing(material.Active, material.Keys); err != nil {
		return material, err
	}
	if len(material.Digest) != 32 {
		return material, ErrConfiguration
	}
	for _, key := range material.Keys {
		if bytes.Equal(key, material.Digest) {
			return material, ErrConfiguration
		}
	}
	return material, nil
}
