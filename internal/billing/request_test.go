package billing

import (
	"strings"
	"testing"
)

func TestStrictRequestRejectsAmbiguousJSONAndNonTextInputs(t *testing.T) {
	chat := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32`
	for _, tc := range []struct {
		name       string
		body       string
		embeddings bool
	}{
		{"duplicate limit", chat + `,"max_tokens":4096}`, false},
		{"escaped duplicate", chat + `,"max_\u0074okens":4096}`, false},
		{"duplicate model", chat + `,"model":"other"}`, false},
		{"nested duplicate", `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"hi","content":null}]}`, false},
		{"nested stream duplicate", chat + `,"stream_options":{"include_usage":true,"include_usage":false}}`, false},
		{"multiple values", chat + `} {}`, false},
		{"invalid syntax", chat + `,}`, false},
		{"invalid UTF8", strings.Replace(chat, "hi", string([]byte{0xff}), 1) + `}`, false},
		{"root null", `null`, false},
		{"root array", `[]`, false},
		{"missing content", `{"model":"m","max_tokens":32,"messages":[{"role":"user"}]}`, false},
		{"null content", `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":null}]}`, false},
		{"null message", `{"model":"m","max_tokens":32,"messages":[null]}`, false},
		{"object name", `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"hi","name":{}}]}`, false},
		{"null user", chat + `,"user":null}`, false},
		{"null stream", chat + `,"stream":null}`, false},
		{"null stream options", chat + `,"stream_options":null}`, false},
		{"null usage option", chat + `,"stream_options":{"include_usage":null}}`, false},
		{"reasoning", chat + `,"reasoning_effort":"high"}`, false},
		{"service tier", chat + `,"service_tier":"priority"}`, false},
		{"embedding null", `{"model":"m","input":null}`, true},
		{"embedding empty", `{"model":"m","input":""}`, true},
		{"embedding empty batch", `{"model":"m","input":[]}`, true},
		{"embedding null item", `{"model":"m","input":["hi",null]}`, true},
		{"embedding empty item", `{"model":"m","input":["hi",""]}`, true},
		{"embedding tokens", `{"model":"m","input":[1,2,3]}`, true},
		{"embedding dimensions", `{"model":"m","input":"hi","dimensions":0}`, true},
		{"embedding encoding", `{"model":"m","input":"hi","encoding_format":null}`, true},
		{"nesting limit", chat + `,"extension":` + strings.Repeat("[", 40) + `0` + strings.Repeat("]", 40) + `}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStrictRequest([]byte(tc.body), tc.embeddings)
			if err == nil || got != (StrictRequest{}) {
				t.Fatalf("unsafe request accepted or partial limits returned: %+v, %v", got, err)
			}
		})
	}
}

func TestStrictRequestRetainsExplicitOutputLimit(t *testing.T) {
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		body := `{"model":"m","messages":[{"role":"user","content":"","name":"tester"}],"` + field + `":32,"n":1,"stream":false,"stream_options":{"include_usage":true}}`
		got, err := ParseStrictRequest([]byte(body), false)
		if err != nil || got.OutputLimit != 32 || got.OutputLimitField != field {
			t.Fatalf("lost explicit limit: %+v %v", got, err)
		}
	}
	for _, input := range []string{`"hi"`, `["hi","there"]`} {
		got, err := ParseStrictRequest([]byte(`{"model":"m","input":`+input+`,"dimensions":256,"encoding_format":"base64"}`), true)
		if err != nil || got != (StrictRequest{}) {
			t.Fatalf("text embedding rejected: %+v %v", got, err)
		}
	}
}
