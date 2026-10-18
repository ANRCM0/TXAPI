package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

func responsesCompletionText(stream bool, body []byte) string {
	if !stream {
		return responsesOutputText(body)
	}
	var deltas strings.Builder
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		var event struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(bytes.TrimSpace(line[5:]), &event) != nil {
			continue
		}
		if len(event.Response) != 0 {
			if text := responsesOutputText(event.Response); text != "" {
				return text
			}
		}
		if event.Type == "response.output_text.delta" || event.Type == "response.function_call_arguments.delta" {
			deltas.WriteString(event.Delta)
		}
	}
	return deltas.String()
}

func responsesOutputText(body []byte) string {
	var response struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
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
		for _, part := range item.Content {
			text.WriteString(part.Text)
			text.WriteString(part.Refusal)
		}
	}
	return text.String()
}
