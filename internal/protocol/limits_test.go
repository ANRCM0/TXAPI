package protocol

import (
	"testing"

	"minigateway/internal/protocol/meta"
)

func TestOutputLimitSurvivesActualConversion(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "gemini"} {
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			t.Run(name+"/"+field, func(t *testing.T) {
				a, ok := Resolve(name)
				if !ok {
					t.Fatal(name)
				}
				m := &meta.Meta{ChannelType: name, OriginModel: "public", ActualModel: "mapped", Mode: meta.ModeChatCompletions}
				body := []byte(`{"model":"public","messages":[{"role":"user","content":"hi"}],"` + field + `":32}`)
				input, conversionErr := Prepare(a, body, m)
				if field == "max_completion_tokens" && name != "openai" {
					if conversionErr == nil {
						t.Fatal("unrepresentable limit was converted")
					}
					return
				}
				if conversionErr != nil {
					t.Fatal(conversionErr)
				}
				err := CheckOutputLimit(a, input.Body, 32, field)
				if wantRejection := field == "max_completion_tokens" && name != "openai"; (err != nil) != wantRejection {
					t.Fatalf("output limit guard: %v, wire=%s", err, input.Body)
				}
			})
		}
	}
}

func TestOutputLimitRejectsMissingChangedAndMultipliedLimits(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, field, body string
	}{
		{"missing", "openai", "max_tokens", `{}`},
		{"null", "openai", "max_tokens", `{"max_tokens":null}`},
		{"increased", "openai", "max_tokens", `{"max_tokens":4096}`},
		{"decreased", "openai", "max_tokens", `{"max_tokens":1}`},
		{"both limits", "openai", "max_tokens", `{"max_tokens":32,"max_completion_tokens":32}`},
		{"changed semantics", "openai", "max_completion_tokens", `{"max_tokens":32}`},
		{"multiple candidates", "openai", "max_tokens", `{"max_tokens":32,"n":2}`},
		{"null candidates", "openai", "max_tokens", `{"max_tokens":32,"n":null}`},
		{"claude default", "anthropic", "max_tokens", `{"max_tokens":4096}`},
		{"gemini missing", "gemini", "max_tokens", `{"generationConfig":{}}`},
		{"gemini null", "gemini", "max_tokens", `{"generationConfig":null}`},
		{"gemini multiple", "gemini", "max_tokens", `{"generationConfig":{"maxOutputTokens":32,"candidateCount":2}}`},
		{"gemini null candidates", "gemini", "max_tokens", `{"generationConfig":{"maxOutputTokens":32,"candidateCount":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := Resolve(tc.protocol)
			if err := CheckOutputLimit(a, []byte(tc.body), 32, tc.field); err == nil {
				t.Fatal("unsafe prepared request accepted")
			}
		})
	}
	a, _ := Resolve("openai")
	for _, tc := range []struct {
		limit int
		field string
	}{{0, "max_tokens"}, {-1, "max_tokens"}, {32, "unknown"}} {
		if err := CheckOutputLimit(a, []byte(`{"max_tokens":32}`), tc.limit, tc.field); err == nil {
			t.Fatal("invalid source limit accepted")
		}
	}
}
