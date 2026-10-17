package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"minigateway/internal/model"
)

func embeddingProfileContract() ProviderCostContract {
	return ProviderCostContract{ChannelID: 1, BaseURL: "https://api.openai.com", Protocol: "openai",
		ActualModel: "text-embedding-3-small", Operation: "embeddings", InputTokenCeiling: 300_000,
		InputBoundProfile: OpenAIEmbeddingsV1, InputPricePer1K: "0.00002", OutputPricePer1K: "0",
		Evidence: "isolated tariff fixture; input cap source recorded separately"}
}

func TestInputBoundProfileRejectsUnprovedTargetsAndUnderstatedCaps(t *testing.T) {
	for _, edit := range []func(*ProviderCostContract){
		func(c *ProviderCostContract) { c.InputBoundProfile = "typo" },
		func(c *ProviderCostContract) { c.InputTokenCeiling = 8192 },
		func(c *ProviderCostContract) { c.InputTokenCeiling = 299999 },
		func(c *ProviderCostContract) { c.BaseURL = "http://api.openai.com" },
		func(c *ProviderCostContract) { c.BaseURL = "https://proxy.invalid" },
		func(c *ProviderCostContract) { c.BaseURL += "/v1" },
		func(c *ProviderCostContract) { c.ActualModel = "embedding-alias" },
		func(c *ProviderCostContract) { c.ActualModel += "-future" },
		func(c *ProviderCostContract) {
			c.Operation = "chat"
			c.OutputLimitField = "max_tokens"
			c.MaxOutputTokens = 32
		},
	} {
		c := embeddingProfileContract()
		edit(&c)
		if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err == nil {
			t.Fatalf("unproved profile accepted: %+v", c)
		}
	}
	for _, model := range []string{"text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002"} {
		c := embeddingProfileContract()
		c.ActualModel = model
		if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInputBoundProfileChecksActualWireAndBatch(t *testing.T) {
	c := embeddingProfileContract()
	const endpoint = "https://api.openai.com/v1/embeddings"
	for _, tc := range []struct {
		url, model string
		count      int
		wantError  bool
	}{
		{endpoint, c.ActualModel, 1, false},
		{endpoint, c.ActualModel, 2048, false},
		{endpoint, c.ActualModel, 2049, true},
		{endpoint, "different-model", 1, true},
		{"https://proxy.invalid/v1/embeddings", c.ActualModel, 1, true},
		{endpoint + "?model=different", c.ActualModel, 1, true},
		{"https://api.openai.com/v1/chat/completions", c.ActualModel, 1, true},
	} {
		input := make([]string, tc.count)
		for i := range input {
			input[i] = "hello"
		}
		body, _ := json.Marshal(map[string]any{"model": tc.model, "input": input})
		err := checkProfileRequest(c, tc.url, body)
		if (err != nil) != tc.wantError {
			t.Fatalf("wire profile admission: %+v err=%v", tc, err)
		}
	}
	if err := checkProfileRequest(c, endpoint, []byte(`{"model":"`+c.ActualModel+`","input":"`+strings.Repeat("x", 10000)+`"}`)); err != nil {
		t.Fatalf("profile should use provider cap, not a character heuristic: %v", err)
	}
}

func TestInputBoundProfileCannotBypassPreparedRequest(t *testing.T) {
	e, st, user, key := strictFixture(t, 1, 1)
	c := embeddingProfileContract()
	if err := e.ConfigureProviderCosts("0.006", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	pre, err := e.Reserve(context.Background(), key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	attempt := model.BillingAttempt{Number: 1, ChannelID: 1, ActualModel: c.ActualModel}
	if err := e.StartCostedAttempt(context.Background(), pre, attempt, providerTestTarget(c), StrictRequest{}); !errors.Is(err, ErrProviderContract) {
		t.Fatalf("profile bypassed prepared request: %v", err)
	}
	detail, err := e.ReservationDetail(context.Background(), pre.BillingID())
	if err != nil || len(detail.Attempts) != 0 || detail.Reservation.ProviderHeldUnits != "0" {
		t.Fatalf("profile bypass consumed cost: %+v %v", detail, err)
	}
	if _, _, err := e.Finish(context.Background(), pre, nil, "", "", false); err != nil {
		t.Fatal(err)
	}
	if readBalance(t, st, "user", user).ReservedUnits != 0 {
		t.Fatal("profile refusal leaked user hold")
	}
}
