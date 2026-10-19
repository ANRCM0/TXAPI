package upstreamauth

import (
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidAccountID(value string) bool {
	if len(value) != 35 || !strings.HasPrefix(value, "ua_") {
		return false
	}
	data, err := hex.DecodeString(value[3:])
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == value[3:]
}

func ValidHostID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	data, err := hex.DecodeString(compact)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == compact && data[6]>>4 == 4 && data[8]>>6 == 2
}

func ValidName(value string) bool {
	if len(value) == 0 || len(value) > 128 || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ValidWireText(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for _, b := range []byte(value) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}
