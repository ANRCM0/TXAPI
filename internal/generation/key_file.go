package generation

import (
	"crypto/sha256"
	"encoding/hex"

	state "minigateway/internal/foundation/contracts/state/v1"
	"minigateway/internal/secrets"
)

type ContentKeyConfig struct {
	Active string
	Keys   map[string][]byte
	Digest []byte
}

// ReadContentKeyFile accepts mounted key material using the shared strict parser.
func ReadContentKeyFile(path string) (ContentKeyConfig, error) {
	key, err := secrets.ReadKeyFile(path)
	if err != nil {
		return ContentKeyConfig{}, err
	}
	return ContentKeyConfig{Active: key.Active, Keys: key.Keys, Digest: key.Digest}, nil
}

func (k ContentKeyConfig) ValidateRequirements(requirements []state.GenerationKeyRequirement, digestVersion string) error {
	if _, err := secrets.NewRing(k.Active, k.Keys); err != nil {
		return err
	}
	if len(k.Digest) != 32 || len(requirements) > 16 {
		return ErrIntentIntegrity
	}
	digest := sha256.Sum256(k.Digest)
	version := hex.EncodeToString(digest[:])
	if digestVersion != "" && digestVersion != version {
		return ErrIntentIntegrity
	}
	for _, required := range requirements {
		header := required.Header
		if len(header) < 3 || header[0] != 1 || header[1] < 1 || header[1] > 32 || len(header) != 2+int(header[1]) || len(k.Keys[string(header[2:])]) != 32 ||
			(required.DigestVersion != "" && required.DigestVersion != version) {
			return ErrIntentIntegrity
		}
	}
	return nil
}
