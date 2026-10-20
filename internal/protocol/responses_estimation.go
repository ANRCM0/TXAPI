package protocol

import (
	"encoding/json"
	"strings"
)

func responsesCompletionText(stream bool, body []byte) string {
	if !stream {
		return responsesOutputText(body)
	}
	var deltas strings.Builder
	var final string
	visitSSEData(body, func(data []byte) bool {
		var event struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil {
			return true
		}
		terminal := event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed" || event.Type == "response.cancelled"
		if terminal && len(event.Response) != 0 {
			if text := responsesOutputText(event.Response); text != "" {
				final = text
				return false
			}
		}
		if event.Type == "response.output_text.delta" || event.Type == "response.function_call_arguments.delta" || event.Type == "response.custom_tool_call_input.delta" {
			deltas.WriteString(event.Delta)
		}
		return !terminal && event.Type != "error"
	})
	if final != "" {
		return final
	}
	return deltas.String()
}

func responsesOutputText(body []byte) string {
	var response struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
			Input     string `json:"input"`
			Content   []struct {
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	var text strings.Builder
	for _, item := range response.Output {
		if item.Type == "function_call" {
			text.WriteString(item.Arguments)
		}
		if item.Type == "custom_tool_call" {
			text.WriteString(item.Input)
		}
		for _, part := range item.Content {
			text.WriteString(part.Text)
			text.WriteString(part.Refusal)
		}
	}
	return text.String()
}
