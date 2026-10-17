package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minigateway/internal/core"
	"minigateway/internal/protocol/meta"
)

func TestGeminiUsageTailRequiresAnIndependentTerminalState(t *testing.T) {
	start := `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}` + "\n\n"
	finish := `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}` + "\n\n"
	tail := `data: {"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}` + "\n\n"
	for _, tc := range []struct {
		name    string
		body    string
		readErr bool
		ok      bool
	}{
		{name: "final usage after finish", body: start + finish + tail, ok: true},
		{name: "usage cannot replace finish", body: start + tail},
		{name: "read failure after finish and usage", body: start + finish + tail, readErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tc.body)
			if tc.readErr {
				body = &errorAfterData{data: []byte(tc.body)}
			}
			a, _ := Resolve("gemini")
			executor := core.New(core.Options{Client: &http.Client{Transport: staticResponse{body: body}}})
			defer executor.Close()
			rec := httptest.NewRecorder()
			result := executor.Execute(context.Background(), rec, core.Input{URL: "http://upstream.test", Stream: true, Decoder: NewDecoder(a, &meta.Meta{ActualModel: "gemini-test", IsStream: true})})
			if result.Usage == nil || result.Usage.TotalTokens != 7 {
				t.Fatalf("final observed usage lost: %+v", result)
			}
			usageFrames := 0
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var frame struct {
					Choices []json.RawMessage `json:"choices"`
					Usage   *core.Usage       `json:"usage"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Usage != nil {
					usageFrames++
					if frame.Choices == nil || len(frame.Choices) != 0 {
						t.Fatalf("usage-only frame must have choices:[]: %s", line)
					}
				}
			}
			wantUsageFrames := 3
			if tc.name == "usage cannot replace finish" {
				wantUsageFrames = 2
			}
			if usageFrames != wantUsageFrames {
				t.Fatalf("usage frames=%d want=%d", usageFrames, wantUsageFrames)
			}
			if tc.ok {
				if result.Error != nil || !result.ProtocolComplete || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
					t.Fatalf("completed stream: result=%+v body=%s", result, rec.Body.String())
				}
			} else if result.Error == nil || result.ProtocolComplete || strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("failed stream marked complete: result=%+v body=%s", result, rec.Body.String())
			}
		})
	}
}
