package billing

import (
	"errors"
	"strings"
	"testing"

	"minigateway/internal/protocol"
	"minigateway/internal/protocol/meta"
)

func TestPreparedBillingShapeAcceptsActualTextConversions(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "gemini"} {
		for _, text := range []string{"hi", "", "你好", strings.Repeat("x", 8192)} {
			a, _ := protocol.Resolve(name)
			body := []byte(`{"model":"m","messages":[{"role":"system","content":"instruction"},{"role":"user","content":"` + text + `"}],"max_tokens":32,"stream":true,"stream_options":{"include_usage":true}}`)
			input, err := protocol.Prepare(a, body, &meta.Meta{OriginModel: "m", ActualModel: "mapped", Mode: meta.ModeChatCompletions})
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePreparedRequest(input.Body, name, false); err != nil {
				t.Fatalf("%s text conversion rejected: %v wire=%s", name, err, input.Body)
			}
		}
	}
	for _, input := range []string{`"text"`, `["one","two","你好"]`} {
		if err := ValidatePreparedRequest([]byte(`{"model":"m","input":`+input+`,"dimensions":4,"encoding_format":"base64"}`), "openai", true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreparedBillingShapeRejectsHiddenDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
		embeddings           bool
	}{
		{"openai tool", "openai", `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"tools":[]}`, false},
		{"openai tier", "openai", `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"service_tier":"priority"}`, false},
		{"openai reasoning", "openai", `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"reasoning_effort":"high"}`, false},
		{"openai media", "openai", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}],"max_tokens":32}`, false},
		{"openai duplicate", "openai", `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"max_tokens":32}`, false},
		{"embedding token array", "openai", `{"model":"m","input":[1,2,3]}`, true},
		{"embedding mixed batch", "openai", `{"model":"m","input":["one",[1,2]]}`, true},
		{"embedding extra dimension", "openai", `{"model":"m","input":"hi","service_tier":"priority"}`, true},
		{"claude thinking", "anthropic", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":32,"thinking":{"type":"enabled","budget_tokens":1024}}`, false},
		{"claude cache", "anthropic", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"max_tokens":32}`, false},
		{"claude nested tool", "anthropic", `{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x"}]}],"max_tokens":32}`, false},
		{"claude system blocks", "anthropic", `{"model":"m","system":[{"type":"text","text":"hi","cache_control":{}}],"messages":[],"max_tokens":32}`, false},
		{"claude hidden duplicate", "anthropic", `{"model":"m","messages":[{"role":"user","content":[{"type":"image"}]}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":32}`, false},
		{"gemini thinking", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32,"thinkingConfig":{"thinkingBudget":1024}}}`, false},
		{"gemini cache", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32},"cachedContent":"cached/x"}`, false},
		{"gemini inline media", "gemini", `{"contents":[{"parts":[{"text":"hi","inlineData":{"mimeType":"image/png","data":"x"}}]}],"generationConfig":{"maxOutputTokens":32}}`, false},
		{"gemini system tool", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"functionCall":{"name":"x"}}]},"generationConfig":{"maxOutputTokens":32}}`, false},
		{"gemini output media", "gemini", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32,"responseModalities":["IMAGE"]}}`, false},
		{"unknown protocol", "unknown", `{}`, false},
		{"non-openai embedding", "anthropic", `{"input":"hi"}`, true},
		{"null object", "gemini", `null`, false},
		{"trailing JSON", "gemini", `{} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePreparedRequest([]byte(tc.body), tc.protocol, tc.embeddings); !errors.Is(err, ErrUnpricedDimension) {
				t.Fatalf("unpriced wire accepted: %v", err)
			}
		})
	}
}
