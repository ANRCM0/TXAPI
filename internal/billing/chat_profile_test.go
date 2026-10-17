package billing

import (
	"fmt"
	"testing"
)

func TestChatInputProfileBindsSnapshotAndTotalOutputLimit(t *testing.T) {
	c := ProviderCostContract{ChannelID: 1, BaseURL: "https://api.openai.com", Protocol: "openai",
		ActualModel: "gpt-4o-mini-2024-07-18", Operation: "chat", InputTokenCeiling: 128000,
		InputBoundProfile: OpenAIChatV1, OutputLimitField: "max_completion_tokens", MaxOutputTokens: 16384,
		InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006", Evidence: "isolated fixture"}
	if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ProviderCostContract){
		func(c *ProviderCostContract) { c.InputTokenCeiling-- },
		func(c *ProviderCostContract) { c.MaxOutputTokens++ },
		func(c *ProviderCostContract) { c.ActualModel = "gpt-4o-mini" },
		func(c *ProviderCostContract) { c.OutputLimitField = "max_tokens" },
		func(c *ProviderCostContract) { c.BaseURL = "https://proxy.invalid" },
	} {
		changed := c
		edit(&changed)
		if _, err := compileProviderCosts("1", []ProviderCostContract{changed}); err == nil {
			t.Fatalf("unproved contract: %+v", changed)
		}
	}
	for _, tc := range []struct {
		field, model, url string
		limit             int
		wantErr           bool
	}{
		{"max_completion_tokens", c.ActualModel, c.BaseURL + "/v1/chat/completions", 16384, false},
		{"max_completion_tokens", c.ActualModel, c.BaseURL + "/v1/chat/completions", 16385, true},
		{"max_tokens", c.ActualModel, c.BaseURL + "/v1/chat/completions", 32, true},
		{"max_completion_tokens", "gpt-4o-mini", c.BaseURL + "/v1/chat/completions", 32, true},
		{"max_completion_tokens", c.ActualModel, c.BaseURL + "/v1/responses", 32, true},
	} {
		body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"service_tier":"default",%q:%d}`, tc.model, tc.field, tc.limit))
		if err := checkProfileRequest(c, tc.url, body); (err != nil) != tc.wantErr {
			t.Fatalf("wire check: %+v err=%v", tc, err)
		}
	}
	for _, tier := range []string{"", `,"service_tier":"auto"`, `,"service_tier":"priority"`, `,"service_tier":null`} {
		body := []byte(`{"model":"` + c.ActualModel + `","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":32` + tier + `}`)
		if checkProfileRequest(c, c.BaseURL+"/v1/chat/completions", body) == nil {
			t.Fatalf("unbounded project service tier: %s", body)
		}
	}
}
