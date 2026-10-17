package openai

import (
	"encoding/json"
	"reflect"
	"testing"

	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/model"
)

func TestJSONStringContentRetainsItsShape(t *testing.T) {
	a := &Adaptor{}
	for _, text := range []string{"[]", "null", `[{}]`, `[{"type":"text","text":"literal"}]`, `{"result":[1,2]}`, "你好"} {
		t.Run(text, func(t *testing.T) {
			encoded, err := json.Marshal(text)
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{canonical.RoleUser, canonical.RoleTool} {
				t.Run(role, func(t *testing.T) {
					messages := `{"role":"user","content":` + string(encoded) + `}`
					if role == canonical.RoleTool {
						messages = `{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":` + string(encoded) + `}`
					}
					request, err := a.ParseRequest([]byte(`{"model":"m","messages":[` + messages + `]}`))
					if err != nil {
						t.Fatal(err)
					}
					message := request.Messages[len(request.Messages)-1]
					wantType := canonical.PartText
					if role == canonical.RoleTool {
						wantType = canonical.PartToolResult
					}
					if len(message.Content) != 1 || message.Content[0].Type != wantType || message.Content[0].Text != text {
						t.Fatalf("string content changed: %+v", message)
					}
				})
			}
			response, err := a.ParseResponse([]byte(`{"choices":[{"message":{"role":"assistant","content":` + string(encoded) + `}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			want := []canonical.ContentPart{{Type: canonical.PartText, Text: text}}
			if !reflect.DeepEqual(response.Content, want) {
				t.Fatalf("response string changed: %+v", response.Content)
			}
			chunks, err := a.ParseStreamLine([]byte(`data: {"choices":[{"delta":{"content":` + string(encoded) + `}}]}`))
			if err != nil || len(chunks) != 1 || !reflect.DeepEqual(chunks[0].Content, want) {
				t.Fatalf("stream string changed: %+v, %v", chunks, err)
			}
			line, err := a.BuildStreamLine(chunks[0])
			if err != nil {
				t.Fatal(err)
			}
			data, ok := sseData(line)
			var wire struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if !ok || json.Unmarshal([]byte(data), &wire) != nil || len(wire.Choices) != 1 || wire.Choices[0].Delta.Content != text {
				t.Fatalf("outbound stream string changed: %s", line)
			}
		})
	}
}

func TestContentArraysAndNullRemainStructured(t *testing.T) {
	a := &Adaptor{}
	request, err := a.ParseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"[]"},{"type":"image_url","image_url":{"url":"data:image/png;base64,eA=="}},{"type":"text","text":"null"}]},{"role":"assistant","content":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []canonical.ContentPart{
		{Type: canonical.PartText, Text: "[]"},
		{Type: canonical.PartImage, ImageURL: "data:image/png;base64,eA=="},
		{Type: canonical.PartText, Text: "null"},
	}
	if !reflect.DeepEqual(request.Messages[0].Content, want) || len(request.Messages[1].Content) != 0 {
		t.Fatalf("array/null shape changed: %+v", request.Messages)
	}
	for _, raw := range []string{"null", "[]", `""`} {
		response, err := a.ParseResponse([]byte(`{"choices":[{"message":{"content":` + raw + `}}]}`))
		if err != nil || len(response.Content) != 0 {
			t.Fatalf("empty content %s changed: %+v, %v", raw, response, err)
		}
	}
}

func TestUsageOnlyStreamChoicesIsAnEmptyArray(t *testing.T) {
	a := &Adaptor{}
	for _, finish := range []string{"", canonical.FinishStop} {
		line, err := a.BuildStreamLine(&canonical.StreamChunk{FinishReason: finish, Usage: &model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}})
		if err != nil {
			t.Fatal(err)
		}
		data, ok := sseData(line)
		var wire struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *model.Usage `json:"usage"`
		}
		if !ok || json.Unmarshal([]byte(data), &wire) != nil || wire.Choices == nil || wire.Usage == nil || wire.Usage.TotalTokens != 7 {
			t.Fatalf("invalid usage frame: %s", line)
		}
		if finish == "" {
			if len(wire.Choices) != 0 {
				t.Fatalf("usage-only frame contains choices: %s", line)
			}
		} else if len(wire.Choices) != 1 || wire.Choices[0].FinishReason != finish {
			t.Fatalf("usage removed the terminal choice: %s", line)
		}
	}
}
