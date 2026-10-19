package gemini

import (
	"testing"

	"minigateway/internal/protocol/canonical"
)

func TestStreamRejectsBusinessOutputAfterFinish(t *testing.T) {
	for _, terminal := range []string{
		`{"candidates":[{"finishReason":"STOP"}]}`,
		`{"promptFeedback":{"blockReason":"SAFETY"}}`,
	} {
		for _, late := range []string{
			`{"candidates":[{"content":{"parts":[{"text":"late text"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"late_tool","args":{}}}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"late text"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":10}}`,
		} {
			s := newTranslator()
			if _, err := s.ParseStreamLine([]byte("data: " + terminal)); err != nil {
				t.Fatal(err)
			}
			roleSent, nextTool := s.roleSent, s.nextToolIndex
			chunks, err := s.ParseStreamLine([]byte("data: " + late))
			if err == nil || len(chunks) != 0 || s.roleSent != roleSent || s.nextToolIndex != nextTool {
				t.Fatalf("late business output accepted or mutated state: terminal=%s late=%s chunks=%+v err=%v", terminal, late, chunks, err)
			}
		}
	}
}

func TestStreamFinalContentToolFinishUsageRemainOrdered(t *testing.T) {
	s := newTranslator()
	chunks, err := s.ParseStreamLine([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"final text"},{"functionCall":{"name":"lookup","args":{"city":"上海"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`))
	if err != nil || len(chunks) != 5 {
		t.Fatalf("final event lost chunks: %+v %v", chunks, err)
	}
	if chunks[0].Role != canonical.RoleAssistant || textFromChunks(chunks[1:2]) != "final text" || len(chunks[2].ToolCalls) != 1 || chunks[2].ToolCalls[0].Name != "lookup" || chunks[2].ToolCalls[0].Arguments != `{"city":"上海"}` || chunks[3].FinishReason != canonical.FinishToolCalls || chunks[4].Usage == nil || chunks[4].Usage.TotalTokens != 5 {
		t.Fatalf("final content/finish/usage order changed: %+v", chunks)
	}
}

func TestStreamFinishedAllowsUsageAndIgnoresRepeatedFinish(t *testing.T) {
	s := newTranslator()
	if _, err := s.ParseStreamLine([]byte(`data: {"candidates":[{"finishReason":"STOP"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{
		`{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
		`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
		`{"candidates":[{"content":{"parts":[]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
	} {
		chunks, err := s.ParseStreamLine([]byte("data: " + event))
		if err != nil || len(chunks) != 1 || chunks[0].Usage == nil || chunks[0].Usage.TotalTokens != 5 || chunks[0].FinishReason != "" || len(chunks[0].Content) != 0 || len(chunks[0].ToolCalls) != 0 {
			t.Fatalf("usage snapshot or duplicate finish policy changed: event=%s chunks=%+v err=%v", event, chunks, err)
		}
	}
	if chunks, err := s.ParseStreamLine([]byte(`data: {"candidates":[{"finishReason":"STOP"}]}`)); err != nil || len(chunks) != 0 {
		t.Fatalf("duplicate finish republished: %+v %v", chunks, err)
	}
}
