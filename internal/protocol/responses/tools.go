package responses

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits apply to the newly supported namespace surface. Legacy top-level
// function declarations retain their existing validation and raw JSON.
func toolName(name string) bool {
	if len(name) == 0 || len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateNamespace(raw json.RawMessage) error {
	if !present(raw) {
		return nil
	}
	var name string
	if json.Unmarshal(raw, &name) != nil || !toolName(name) {
		return errors.New("invalid Responses function namespace")
	}
	return nil
}

func validateTools(raw json.RawMessage) error {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return errors.New("Responses tools must be an array")
	}
	groups := map[string]bool{}
	for _, raw := range tools {
		var tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &tool) != nil {
			return errors.New("invalid Responses tool")
		}
		if tool.Type == "function" && tool.Name != "" {
			continue
		}
		if tool.Type != "namespace" {
			return errors.New("Responses supports only named function tools and function namespaces")
		}
		var group struct {
			Description json.RawMessage   `json:"description"`
			Tools       []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(raw, &group) != nil || !toolName(tool.Name) || groups[tool.Name] || len(groups) >= 64 || len(group.Tools) == 0 || len(group.Tools) > 128 {
			return errors.New("invalid or duplicate Responses namespace declaration")
		}
		if present(group.Description) {
			var description string
			if json.Unmarshal(group.Description, &description) != nil {
				return errors.New("Responses namespace description must be text")
			}
		}
		groups[tool.Name] = true
		names := map[string]bool{}
		for _, raw := range group.Tools {
			var member struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &member) != nil || member.Type != "function" || !toolName(member.Name) || names[member.Name] {
				return errors.New("Responses namespace requires unique named function members")
			}
			names[member.Name] = true
		}
	}
	return nil
}

func validateOutputNamespace(raw json.RawMessage) error {
	var item struct {
		Type      string          `json:"type"`
		Namespace json.RawMessage `json:"namespace"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return errors.New("invalid Responses output item")
	}
	if item.Type == "function_call" {
		return validateNamespace(item.Namespace)
	}
	return nil
}
