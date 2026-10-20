package responses

import (
	"encoding/json"
	"errors"
)

// Search and loaded schemas stay opaque JSON. The gateway carries the client's
// discovery conversation; it never performs search or executes a loaded tool.
func searchObject(raw json.RawMessage, optional bool) bool {
	if optional && !present(raw) {
		return true
	}
	var object map[string]json.RawMessage
	return len(raw) <= 64<<10 && json.Unmarshal(raw, &object) == nil && object != nil
}

func validateDeferred(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("invalid Responses tool")
	}
	if value, ok := fields["defer_loading"]; ok {
		var deferred bool
		if !present(value) || json.Unmarshal(value, &deferred) != nil {
			return errors.New("Responses defer_loading must be boolean")
		}
	}
	return nil
}

func validateSearchTool(raw json.RawMessage) error {
	var tool struct {
		Execution   string
		Description json.RawMessage
		Parameters  json.RawMessage
	}
	if json.Unmarshal(raw, &tool) != nil || tool.Execution != "client" || !searchObject(tool.Parameters, true) {
		return errors.New("Responses tool_search requires client execution and bounded object parameters")
	}
	if present(tool.Description) {
		var description string
		if json.Unmarshal(tool.Description, &description) != nil {
			return errors.New("Responses tool_search description must be text")
		}
	}
	return nil
}

func searchCallID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, b := range []byte(id) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func validateSearchItem(raw json.RawMessage) error {
	var item struct {
		Type      string
		Role      string
		Execution string
		CallID    string `json:"call_id"`
		Status    *string
		Arguments json.RawMessage
		Tools     json.RawMessage
	}
	if json.Unmarshal(raw, &item) != nil {
		return errors.New("invalid Responses tool search item")
	}
	if item.Type == "additional_tools" {
		if item.Role != "developer" {
			return errors.New("Responses additional_tools requires developer role")
		}
		return validateLoadedTools(item.Tools)
	}
	if item.Execution != "client" || !searchCallID(item.CallID) {
		return errors.New("Responses tool search item requires client execution and call_id")
	}
	if item.Type == "tool_search_output" {
		if item.Status == nil || *item.Status != "completed" {
			return errors.New("Responses tool search output must be completed")
		}
		return validateLoadedTools(item.Tools)
	}
	if item.Type != "tool_search_call" || !searchObject(item.Arguments, false) ||
		item.Status != nil && *item.Status != "in_progress" && *item.Status != "completed" && *item.Status != "incomplete" {
		return errors.New("invalid Responses tool search call arguments or status")
	}
	return nil
}

func validateLoadedTools(raw json.RawMessage) error {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil || len(tools) > 128 {
		return errors.New("Responses loaded tools require an array of at most 128 tools")
	}
	var validateLoaded func(json.RawMessage) error
	validateLoaded = func(raw json.RawMessage) error {
		var tool struct {
			Type        string
			Name        string
			Description json.RawMessage
			Parameters  json.RawMessage
			Strict      json.RawMessage
			Tools       []json.RawMessage
		}
		if json.Unmarshal(raw, &tool) != nil || !toolName(tool.Name) {
			return errors.New("invalid Responses loaded tool name")
		}
		switch tool.Type {
		case "namespace":
			// validateTools has already rejected nested namespaces and bounds
			// group/member counts. Only descend into those validated members.
			for _, member := range tool.Tools {
				if err := validateLoaded(member); err != nil {
					return err
				}
			}
			return nil
		case "function", "custom":
		default:
			return errors.New("Responses loaded tools support only function/custom and one-level namespaces")
		}
		if present(tool.Description) {
			var description string
			if json.Unmarshal(tool.Description, &description) != nil {
				return errors.New("Responses loaded tool description must be text")
			}
		}
		if tool.Type == "function" {
			if !searchObject(tool.Parameters, true) {
				return errors.New("Responses loaded function parameters must be a bounded object")
			}
			if present(tool.Strict) {
				var strict bool
				if json.Unmarshal(tool.Strict, &strict) != nil {
					return errors.New("Responses loaded function strict must be boolean")
				}
			}
		}
		return nil
	}
	// Prevent search declarations from recurring through an output, even though
	// the same existing shape validator handles normal top-level tools.
	for _, raw := range tools {
		var tool struct{ Type string }
		if json.Unmarshal(raw, &tool) != nil || tool.Type != "function" && tool.Type != "custom" && tool.Type != "namespace" {
			return errors.New("unsupported Responses loaded tool type")
		}
	}
	if err := validateTools(raw); err != nil {
		return err
	}
	for _, raw := range tools {
		if err := validateLoaded(raw); err != nil {
			return err
		}
	}
	return nil
}
