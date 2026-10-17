package generation

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

func compileContentKeys(active string, keys map[string][]byte) (map[string]cipher.AEAD, error) {
	if len(keys) == 0 || len(keys) > 8 {
		return nil, fmt.Errorf("invalid generation content key configuration")
	}
	compiled := make(map[string]cipher.AEAD, len(keys))
	for id, key := range keys {
		if len(id) < 1 || len(id) > 32 || len(key) != 32 {
			return nil, fmt.Errorf("invalid generation content key configuration")
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, fmt.Errorf("invalid generation content key identifier")
			}
		}
		block, err := aes.NewCipher(append([]byte(nil), key...))
		if err != nil {
			return nil, fmt.Errorf("invalid generation content key configuration")
		}
		compiled[id], err = cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("invalid generation content cipher configuration")
		}
	}
	if compiled[active] == nil {
		return nil, fmt.Errorf("active generation content key is missing")
	}
	return compiled, nil
}
