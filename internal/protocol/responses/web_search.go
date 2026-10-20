package responses

import (
	"encoding/json"
	"errors"
	"strings"
)

func hasWebSearch(raw json.RawMessage) bool {
	var tools []struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &tools)
	for _, tool := range tools {
		if tool.Type == "web_search" {
			return true
		}
	}
	return false
}

func validateWebSearchTool(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("invalid Responses web search declaration")
	}
	for key := range fields {
		if key != "type" && key != "external_web_access" && key != "search_content_types" {
			return errors.New("unsupported Responses web search option")
		}
	}
	var access bool
	if !present(fields["external_web_access"]) || json.Unmarshal(fields["external_web_access"], &access) != nil || access {
		return errors.New("Responses web search requires explicit external_web_access=false")
	}
	if raw, ok := fields["search_content_types"]; ok {
		var kinds []string
		if json.Unmarshal(raw, &kinds) != nil || len(kinds) < 1 || len(kinds) > 2 {
			return errors.New("invalid Responses search content types")
		}
		seen := map[string]bool{}
		for _, kind := range kinds {
			if kind != "text" && kind != "image" || seen[kind] {
				return errors.New("invalid Responses search content type")
			}
			seen[kind] = true
		}
	}
	return nil
}

func validateWebSearchChoice(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 {
		return errors.New("invalid Responses web search choice")
	}
	return nil
}

// Search is performed by the upstream. Validation never resolves item IDs or URLs.
func validateWebSearchCall(raw json.RawMessage, input bool) error {
	var item struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		Action json.RawMessage `json:"action"`
	}
	if json.Unmarshal(raw, &item) != nil || !toolName(item.ID) {
		return errors.New("invalid Responses web search call identity")
	}
	switch item.Status {
	case "in_progress", "searching", "completed", "failed":
	default:
		return errors.New("invalid Responses web search status")
	}
	if len(item.Action) == 0 {
		if input || item.Status == "completed" {
			return errors.New("Responses web search call requires a self-contained action")
		}
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(item.Action, &fields) != nil || fields == nil {
		return errors.New("invalid Responses web search action")
	}
	for _, key := range []string{"type", "query", "queries", "url", "pattern", "sources"} {
		if raw, ok := fields[key]; ok && !present(raw) {
			return errors.New("Responses web search action fields cannot be null")
		}
	}
	var action struct {
		Type    string   `json:"type"`
		Query   *string  `json:"query"`
		Queries []string `json:"queries"`
		URL     string   `json:"url"`
		Pattern string   `json:"pattern"`
		Sources []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"sources"`
	}
	if json.Unmarshal(item.Action, &action) != nil {
		return errors.New("invalid Responses web search action")
	}
	validText := func(value string) bool { return strings.TrimSpace(value) != "" && len(value) <= 64<<10 }
	switch action.Type {
	case "search":
		if action.Query == nil && len(action.Queries) == 0 || len(action.Queries) > 128 {
			return errors.New("Responses search action requires queries")
		}
		if action.Query != nil && !validText(*action.Query) {
			return errors.New("invalid Responses search query")
		}
		for _, query := range action.Queries {
			if !validText(query) {
				return errors.New("invalid Responses search query")
			}
		}
	case "open_page":
		if !validText(action.URL) {
			return errors.New("Responses open_page action requires url")
		}
	case "find_in_page":
		if !validText(action.URL) || !validText(action.Pattern) {
			return errors.New("Responses find_in_page action requires url and pattern")
		}
	default:
		return errors.New("unsupported Responses web search action")
	}
	if len(action.Sources) > 1024 {
		return errors.New("Responses web search sources exceed limit")
	}
	for _, source := range action.Sources {
		if source.Type != "url" || !validText(source.URL) {
			return errors.New("invalid Responses web search source")
		}
	}
	return nil
}
