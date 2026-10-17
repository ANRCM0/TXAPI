package openai

import (
	"encoding/json"
	"fmt"
	"minigateway/internal/protocol/jsonutil"
)

// This parser is used only by semantic conversion. The native OpenAI fast
// path keeps the original payload, including extensions this model cannot hold.
func validateConvertibleRequest(body []byte) error {
	if err := jsonutil.Validate(body); err != nil {
		return err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	if err := onlyFields(request, "model", "messages", "max_tokens", "temperature", "top_p", "stop", "stream", "stream_options", "tools", "n"); err != nil {
		return err
	}
	if raw, ok := request["stream_options"]; ok {
		var options map[string]json.RawMessage
		var include bool
		if json.Unmarshal(raw, &options) != nil || options == nil || json.Unmarshal(options["include_usage"], &include) != nil || !include {
			return fmt.Errorf("converted streams require include_usage=true when stream_options is specified")
		}
		if err := onlyFields(options, "include_usage"); err != nil {
			return err
		}
	}
	var messages []map[string]json.RawMessage
	if raw, ok := request["messages"]; ok {
		if err := json.Unmarshal(raw, &messages); err != nil {
			return err
		}
	}
	for _, message := range messages {
		if err := onlyFields(message, "role", "content", "name", "tool_call_id", "tool_calls"); err != nil {
			return err
		}
		var role, name string
		if err := json.Unmarshal(message["role"], &role); err != nil {
			return err
		}
		if raw, ok := message["name"]; ok {
			if err := json.Unmarshal(raw, &name); err != nil {
				return err
			}
			if name != "" && role != "tool" {
				return fmt.Errorf("message name cannot be preserved for role %q", role)
			}
		}
		if raw := message["content"]; len(raw) > 0 && string(raw) != "null" {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				if role == "tool" {
					return fmt.Errorf("tool results require string content")
				}
				var parts []map[string]json.RawMessage
				if err := json.Unmarshal(raw, &parts); err != nil {
					return err
				}
				for _, part := range parts {
					var kind string
					if err := json.Unmarshal(part["type"], &kind); err != nil {
						return err
					}
					switch kind {
					case "text":
						if err := onlyFields(part, "type", "text"); err != nil {
							return err
						}
						if err := json.Unmarshal(part["text"], &text); err != nil {
							return err
						}
					case "image_url":
						if err := onlyFields(part, "type", "image_url"); err != nil {
							return err
						}
						var image map[string]json.RawMessage
						if err := json.Unmarshal(part["image_url"], &image); err != nil {
							return err
						}
						if err := onlyFields(image, "url"); err != nil {
							return err
						}
						if err := json.Unmarshal(image["url"], &text); err != nil {
							return err
						}
					default:
						return fmt.Errorf("content part %q cannot be preserved", kind)
					}
				}
			}
		}
		var calls []map[string]json.RawMessage
		if raw, ok := message["tool_calls"]; ok {
			if role != "assistant" {
				return fmt.Errorf("tool_calls require assistant role")
			}
			if err := json.Unmarshal(raw, &calls); err != nil {
				return err
			}
		}
		for _, call := range calls {
			if err := onlyFields(call, "id", "type", "function"); err != nil {
				return err
			}
			var id, kind string
			if json.Unmarshal(call["id"], &id) != nil || id == "" || json.Unmarshal(call["type"], &kind) != nil || kind != "function" {
				return fmt.Errorf("only named function calls with a nonempty id can be converted")
			}
			var function map[string]json.RawMessage
			if err := json.Unmarshal(call["function"], &function); err != nil {
				return err
			}
			if err := onlyFields(function, "name", "arguments"); err != nil {
				return err
			}
			var arguments string
			if json.Unmarshal(function["name"], &name) != nil || name == "" || json.Unmarshal(function["arguments"], &arguments) != nil {
				return fmt.Errorf("invalid function call")
			}
			var object map[string]json.RawMessage
			if json.Unmarshal([]byte(arguments), &object) != nil || object == nil {
				return fmt.Errorf("function arguments must be a JSON object")
			}
		}
	}
	var tools []map[string]json.RawMessage
	if raw, ok := request["tools"]; ok {
		if err := json.Unmarshal(raw, &tools); err != nil {
			return err
		}
	}
	for _, tool := range tools {
		if err := onlyFields(tool, "type", "function"); err != nil {
			return err
		}
		var kind, name string
		if json.Unmarshal(tool["type"], &kind) != nil || kind != "function" {
			return fmt.Errorf("only function tools can be converted")
		}
		var function map[string]json.RawMessage
		if err := json.Unmarshal(tool["function"], &function); err != nil {
			return err
		}
		if err := onlyFields(function, "name", "description", "parameters"); err != nil {
			return err
		}
		if json.Unmarshal(function["name"], &name) != nil || name == "" {
			return fmt.Errorf("tool name is required")
		}
	}
	return nil
}

func onlyFields(object map[string]json.RawMessage, allowed ...string) error {
	for field := range object {
		found := false
		for _, name := range allowed {
			if name == field {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("field %q cannot be preserved by conversion", field)
		}
	}
	return nil
}
