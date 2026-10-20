package responses

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HasCompactionTrigger reads only the validated native input, never metadata hints.
func (r Request) HasCompactionTrigger() bool {
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(r.Input, &items) != nil {
		return false
	}
	for _, item := range items {
		if item.Type == "compaction_trigger" {
			return true
		}
	}
	return false
}

func validateCompactionTrigger(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	var kind string
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || json.Unmarshal(fields["type"], &kind) != nil || kind != "compaction_trigger" {
		return errors.New("invalid Responses compaction trigger")
	}
	return nil
}

// Opaque bytes remain provider-owned. Pending additions may omit them; completed
// items and replay input must be self-contained and never resolve a bare ID.
func validateCompactionItem(raw json.RawMessage, complete bool) error {
	var fields map[string]json.RawMessage
	var kind string
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(fields["type"], &kind) != nil || kind != "compaction" {
		return errors.New("invalid Responses compaction item")
	}
	if raw, ok := fields["id"]; ok {
		var id string
		if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
			return errors.New("invalid Responses compaction item identity")
		}
		for _, r := range id {
			if unicode.IsControl(r) {
				return errors.New("invalid Responses compaction item identity")
			}
		}
	}
	content, exists := fields["encrypted_content"]
	if !exists && !complete {
		return nil
	}
	var opaque string
	if !exists || json.Unmarshal(content, &opaque) != nil || opaque == "" || len(opaque) > maxEventBytes {
		return errors.New("Responses compaction item requires bounded encrypted_content")
	}
	return nil
}
