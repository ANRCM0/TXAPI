package gemini

import (
	"strings"
	"testing"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/canonical"
)

func textFromChunks(chunks []*canonical.StreamChunk) string {
	var text strings.Builder
	for _, chunk := range chunks {
		for _, part := range chunk.Content {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

// Literal streamGenerateContent events follow the official SDK's chunk-by-chunk
// concatenation, independently of our response structs and fake upstream.
func TestStreamPreservesIndependentUTF8Deltas(t *testing.T) {
	events := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"世界"}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"，"}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"stream"}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":9,"totalTokenCount":21}}`,
	}
	wantDeltas := []string{"你好", "世界", "，", "stream", "!"}
	session := (&Adaptor{}).NewStreamSession()
	var got strings.Builder
	roles, finishes := 0, 0
	for i, event := range events {
		chunks, err := session.ParseStreamLine([]byte(event))
		if err != nil {
			t.Fatal(err)
		}
		if delta := textFromChunks(chunks); delta != wantDeltas[i] {
			t.Errorf("event %d delta = %q, want %q", i, delta, wantDeltas[i])
		}
		got.WriteString(textFromChunks(chunks))
		for _, chunk := range chunks {
			if chunk.Role == canonical.RoleAssistant {
				roles++
			}
			if chunk.FinishReason != "" {
				finishes++
			}
		}
		if i == len(events)-1 {
			if len(chunks) != 3 || chunks[1].FinishReason != canonical.FinishStop || chunks[2].Usage == nil || chunks[2].Usage.TotalTokens != 21 {
				t.Fatalf("final event must emit text, finish, usage in order: %+v", chunks)
			}
		}
	}
	if got.String() != "你好世界，stream!" || roles != 1 || finishes != 1 {
		t.Fatalf("text=%q roles=%d finishes=%d", got.String(), roles, finishes)
	}
}

func TestStreamSessionsAreRequestLocal(t *testing.T) {
	shared := &Adaptor{}
	first, second := shared.NewStreamSession(), shared.NewStreamSession()
	start := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)
	finish := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"!"}]},"finishReason":"STOP"}]}`)
	for _, session := range []adaptor.StreamSession{first, second} {
		chunks, err := session.ParseStreamLine(start)
		if err != nil || textFromChunks(chunks) != "hello" || len(chunks) < 1 || chunks[0].Role != canonical.RoleAssistant {
			t.Fatalf("isolated first chunk: %+v err=%v", chunks, err)
		}
		chunks, err = session.ParseStreamLine(finish)
		if err != nil || textFromChunks(chunks) != "!" || len(chunks) < 1 || chunks[len(chunks)-1].FinishReason != canonical.FinishStop {
			t.Fatalf("isolated final chunk: %+v err=%v", chunks, err)
		}
	}
}
