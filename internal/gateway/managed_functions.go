package gateway

import (
	"bytes"
	"encoding/json"

	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
)

const managedFunctionBytes = 64 << 10

func managedFields(fields map[string]json.RawMessage, allowed ...string) bool {
	for key := range fields {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return false
		}
	}
	return fields != nil
}

func managedString(raw json.RawMessage, limit int) (string, bool) {
	var value string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || len(value) > limit {
		return "", false
	}
	return value, true
}

func managedFunctionName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, ch := range []byte(name) {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func managedIdentifier(raw json.RawMessage, limit int) (string, bool) {
	value, ok := managedString(raw, limit)
	if !ok || value == "" {
		return "", false
	}
	for _, ch := range []byte(value) {
		if ch < '!' || ch > '~' {
			return "", false
		}
	}
	return value, true
}

func managedBoolean(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("true")) || bytes.Equal(bytes.TrimSpace(raw), []byte("false"))
}

func managedFunctionObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(raw) <= managedFunctionBytes && len(trimmed) > 0 && trimmed[0] == '{' && jsonvalue.Validate(raw) == nil
}

// This is a source permission boundary, separate from the native codec's
// broader API-key tool surface. Nothing here interprets or executes a schema.
func validateManagedFunctions(fields map[string]json.RawMessage) bool {
	names := map[string]bool{}
	if raw, exists := fields["tools"]; exists {
		var tools []map[string]json.RawMessage
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &tools) != nil || len(tools) > 64 {
			return false
		}
		for _, tool := range tools {
			kind, valid := managedString(tool["type"], 16)
			if !managedFields(tool, "type", "name", "description", "parameters", "strict") || !valid || kind != "function" {
				return false
			}
			name, ok := managedString(tool["name"], 64)
			if !ok || !managedFunctionName(name) || names[name] {
				return false
			}
			names[name] = true
			if raw, exists := tool["description"]; exists {
				if _, ok := managedString(raw, managedFunctionBytes); !ok {
					return false
				}
			}
			if raw, exists := tool["parameters"]; exists && !managedFunctionObject(raw) {
				return false
			}
			if raw, exists := tool["strict"]; exists && !managedBoolean(raw) {
				return false
			}
		}
	}
	if raw, exists := fields["parallel_tool_calls"]; exists && !managedBoolean(raw) {
		return false
	}
	if raw, exists := fields["tool_choice"]; exists {
		if choice, ok := managedString(raw, 16); ok {
			return choice == "auto" || choice == "none" || choice == "required" && len(names) > 0
		}
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) != nil || len(choice) != 2 || !managedFields(choice, "type", "name") {
			return false
		}
		kind, valid := managedString(choice["type"], 16)
		name, ok := managedString(choice["name"], 64)
		return valid && kind == "function" && ok && names[name]
	}
	return true
}

func validateManagedFunctionItem(item map[string]json.RawMessage, kind string, calls map[string]bool) bool {
	callID, ok := managedIdentifier(item["call_id"], 128)
	if !ok {
		return false
	}
	if raw, exists := item["id"]; exists {
		if _, ok := managedIdentifier(raw, 256); !ok {
			return false
		}
	}
	if raw, exists := item["status"]; exists {
		status, ok := managedString(raw, 16)
		if !ok || status != "completed" {
			return false
		}
	}
	if kind == "function_call" {
		if !managedFields(item, "type", "id", "call_id", "name", "arguments", "status") || len(calls) >= 128 {
			return false
		}
		if _, exists := calls[callID]; exists {
			return false
		}
		name, ok := managedString(item["name"], 64)
		arguments, valid := managedString(item["arguments"], managedFunctionBytes)
		if !ok || !managedFunctionName(name) || !valid || !managedFunctionObject([]byte(arguments)) {
			return false
		}
		calls[callID] = false
		return true
	}
	if !managedFields(item, "type", "id", "call_id", "output", "status") {
		return false
	}
	resolved, exists := calls[callID]
	if _, valid := managedString(item["output"], managedFunctionBytes); !exists || resolved || !valid {
		return false
	}
	calls[callID] = true
	return true
}
