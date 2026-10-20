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
	search := false
	for _, raw := range tools {
		var tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &tool) != nil {
			return errors.New("invalid Responses tool")
		}
		if tool.Type == "function" && tool.Name != "" {
			if err := validateDeferred(raw); err != nil {
				return err
			}
			continue
		}
		if tool.Type == "custom" {
			if err := validateCustomTool(raw); err != nil {
				return err
			}
			if err := validateDeferred(raw); err != nil {
				return err
			}
			continue
		}
		if tool.Type == "tool_search" {
			if search {
				return errors.New("Responses permits at most one client tool_search")
			}
			if err := validateSearchTool(raw); err != nil {
				return err
			}
			search = true
			continue
		}
		if tool.Type != "namespace" {
			return errors.New("Responses supports only function/custom namespaces and client tool_search")
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
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if _, ok := fields["defer_loading"]; ok {
			return errors.New("Responses namespace deferral belongs on members")
		}
		names := map[string]bool{}
		for _, raw := range group.Tools {
			var member struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &member) != nil || member.Type != "function" && member.Type != "custom" || !toolName(member.Name) || names[member.Name] {
				return errors.New("Responses namespace requires unique named function/custom members")
			}
			if member.Type == "custom" {
				if err := validateCustomTool(raw); err != nil {
					return err
				}
			}
			if err := validateDeferred(raw); err != nil {
				return err
			}
			names[member.Name] = true
		}
	}
	return nil
}

func validateOutputToolItem(raw json.RawMessage) error {
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
	if item.Type == "custom_tool_call" {
		return validateCustomCall(raw)
	}
	if item.Type == "tool_search_call" || item.Type == "tool_search_output" {
		return validateSearchItem(raw)
	}
	return nil
}

func validateCustomTool(raw json.RawMessage) error {
	var tool struct {
		Name        string          `json:"name"`
		Description json.RawMessage `json:"description"`
		Format      json.RawMessage `json:"format"`
	}
	if json.Unmarshal(raw, &tool) != nil || !toolName(tool.Name) {
		return errors.New("invalid Responses custom tool name")
	}
	if present(tool.Description) {
		var description string
		if json.Unmarshal(tool.Description, &description) != nil {
			return errors.New("Responses custom description must be text")
		}
	}
	if !present(tool.Format) {
		return nil
	}
	var format struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if json.Unmarshal(tool.Format, &format) != nil {
		return errors.New("invalid Responses custom format")
	}
	switch format.Type {
	case "text":
		return nil
	case "grammar":
		if (format.Syntax == "lark" || format.Syntax == "regex") && len(format.Definition) > 0 && len(format.Definition) <= 64<<10 {
			return nil
		}
	}
	return errors.New("unsupported Responses custom format or grammar limit")
}

func validateCustomCall(raw json.RawMessage) error {
	var item struct {
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Input     *string         `json:"input"`
		Namespace json.RawMessage `json:"namespace"`
	}
	if json.Unmarshal(raw, &item) != nil || item.CallID == "" || !toolName(item.Name) || item.Input == nil {
		return errors.New("invalid Responses custom tool call")
	}
	return validateNamespace(item.Namespace)
}

func validateCustomOutput(raw json.RawMessage) error {
	var item struct {
		CallID    string          `json:"call_id"`
		Name      *string         `json:"name"`
		Output    json.RawMessage `json:"output"`
		Namespace json.RawMessage `json:"namespace"`
	}
	if json.Unmarshal(raw, &item) != nil || item.CallID == "" || item.Name != nil && !toolName(*item.Name) {
		return errors.New("invalid Responses custom tool output")
	}
	if err := validateNamespace(item.Namespace); err != nil {
		return err
	}
	return validateTextContent(item.Output)
}
