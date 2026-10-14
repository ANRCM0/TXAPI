package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/canonical"
)

func geminiTextLine(t *testing.T, text string, finish string) []byte {
	t.Helper()
	response := geminiResponse{Candidates: []geminiCandidate{{
		Content:      geminiContent{Parts: []geminiPart{{Text: text}}},
		FinishReason: finish,
	}}}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("data: "), data...)
}

func textFromChunks(chunks []*canonical.StreamChunk) string {
	var text strings.Builder
	for _, chunk := range chunks {
		for _, part := range chunk.Content {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func TestStreamDiffsCumulativeUTF8Snapshots(t *testing.T) {
	translator := newTranslator()
	snapshots := []string{"你好", "你好世界!", "你好世界!继续生成", "你好世界!继续生成。"}
	wantDeltas := []string{"你好", "世界!", "继续生成", "。"}
	var got strings.Builder
	for i, snapshot := range snapshots {
		finish := ""
		if i == len(snapshots)-1 {
			finish = "STOP"
		}
		chunks, err := translator.ParseStreamLine(geminiTextLine(t, snapshot, finish))
		if err != nil {
			t.Fatalf("ParseStreamLine: %v", err)
		}
		if delta := textFromChunks(chunks); delta != wantDeltas[i] {
			t.Errorf("chunk %d delta = %q, want %q", i, delta, wantDeltas[i])
		}
		got.WriteString(textFromChunks(chunks))
	}
	if want := snapshots[len(snapshots)-1]; got.String() != want {
		t.Fatalf("concatenated text = %q, want %q", got.String(), want)
	}
}

func TestStreamDiffsEqualAndVariableLengthSuffixes(t *testing.T) {
	translator := newTranslator()
	snapshots := []string{"a", "ab", "abcd", "abcdefg"}
	want := []string{"a", "b", "cd", "efg"}
	for i, snapshot := range snapshots {
		chunks, err := translator.ParseStreamLine(geminiTextLine(t, snapshot, ""))
		if err != nil {
			t.Fatalf("ParseStreamLine: %v", err)
		}
		if got := textFromChunks(chunks); got != want[i] {
			t.Errorf("snapshot %q delta = %q, want %q", snapshot, got, want[i])
		}
	}
}

func TestStreamSessionsAreRequestLocal(t *testing.T) {
	sharedAdaptor := &Adaptor{}
	first := sharedAdaptor.NewStreamSession()
	second := sharedAdaptor.NewStreamSession()

	for _, tc := range []struct {
		session  adaptor.StreamSession
		snapshot string
	}{{first, "alpha"}, {second, "beta"}} {
		chunks, err := tc.session.ParseStreamLine(geminiTextLine(t, tc.snapshot, ""))
		if err != nil {
			t.Fatal(err)
		}
		if got := textFromChunks(chunks); got != tc.snapshot {
			t.Fatalf("initial delta = %q, want %q", got, tc.snapshot)
		}
	}

	for _, tc := range []struct {
		session  adaptor.StreamSession
		snapshot string
	}{{first, "alpha!"}, {second, "beta!"}} {
		chunks, err := tc.session.ParseStreamLine(geminiTextLine(t, tc.snapshot, "STOP"))
		if err != nil {
			t.Fatal(err)
		}
		if got := textFromChunks(chunks); got != "!" {
			t.Fatalf("later delta = %q, want !", got)
		}
	}
}
