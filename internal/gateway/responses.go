package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/responses"
)

// Non-200 bodies bypass the native decoder. Only trusted local facts select an
// error description; no provider message, type, code or transport detail escapes.
func sanitizeResponsesFailure(result core.Result, nativeStatus string) core.Result {
	result.Replayable = false
	if result.Error == nil {
		return result
	}
	kind, message := "upstream_response_error", "Responses upstream request failed"
	switch result.Termination {
	case core.Canceled:
		kind, message = "canceled", "Responses request canceled"
	case core.Deadline:
		kind, message = "deadline", "Responses request time budget exhausted"
	case core.ClientWriteError:
		kind, message = "client_write_error", "Responses output delivery failed"
	default:
		if result.ProtocolComplete && (nativeStatus == "failed" || nativeStatus == "cancelled") {
			kind, message = "upstream_response_failed", "Responses generation did not complete successfully"
		}
	}
	result.Error = &core.Error{StatusCode: result.StatusCode, Error: core.ErrorDetail{Message: message, Type: kind}}
	return result
}

func requestPromptText(req protocol.Request) string {
	if req.Operation() == protocol.Responses {
		parsed, err := responses.ParseRequest(req.Body())
		if err == nil {
			return parsed.PromptText()
		}
		return ""
	}
	return billing.PromptTextFromBody(req.Body())
}

func requestMaxTokens(req protocol.Request) int {
	if req.Operation() == protocol.Responses {
		parsed, err := responses.ParseRequest(req.Body())
		if err == nil {
			return parsed.MaxOutputTokens()
		}
		return 0
	}
	return req.LegacyMaxTokens()
}

func requestCompletionText(req protocol.Request, body []byte) string {
	if req.Operation() != protocol.Responses {
		return billing.CompletionTextFromResponse(body)
	}
	if !req.Stream() {
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
