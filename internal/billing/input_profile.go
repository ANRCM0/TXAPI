package billing

import (
	"encoding/json"
	"errors"
)

// Source and assumptions are recorded under strict billing in docs/PLAN.md.
const OpenAIEmbeddingsV1 = "openai_embeddings_v1"
const OpenAIChatV1 = "openai_chat_v1"

const openAIEmbeddingRequestTokens int64 = 300_000

func validateInputBoundProfile(c ProviderCostContract) error {
	switch c.InputBoundProfile {
	case "":
		return nil
	case OpenAIChatV1:
		if c.Protocol != "openai" || c.Operation != "chat" || c.BaseURL != "https://api.openai.com" ||
			c.ActualModel != "gpt-4o-mini-2024-07-18" || c.InputTokenCeiling < 128_000 ||
			c.MaxOutputTokens > 16_384 || c.OutputLimitField != "max_completion_tokens" {
			return errors.New("openai_chat_v1 requires the official dated GPT-4o Mini chat endpoint, 128000 input ceiling and max_completion_tokens <= 16384")
		}
		return nil
	case OpenAIEmbeddingsV1:
		if c.Protocol != "openai" || c.Operation != "embeddings" || c.BaseURL != "https://api.openai.com" || c.InputTokenCeiling < openAIEmbeddingRequestTokens {
			return errors.New("openai_embeddings_v1 requires the official embeddings endpoint and at least 300000 whole-request input tokens")
		}
		switch c.ActualModel {
		case "text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002":
			return nil
		default:
			return errors.New("model is not covered by openai_embeddings_v1")
		}
	default:
		return errors.New("unknown provider input bound profile")
	}
}

func checkProfileRequest(c ProviderCostContract, preparedURL string, body []byte) error {
	if c.InputBoundProfile == OpenAIChatV1 {
		if preparedURL != "https://api.openai.com/v1/chat/completions" {
			return ErrProviderLimit
		}
		request, err := ParseStrictRequest(body, false)
		if err != nil || request.OutputLimitField != "max_completion_tokens" || request.OutputLimit > 16_384 {
			return ErrProviderLimit
		}
		var wire struct {
			Model       string `json:"model"`
			ServiceTier string `json:"service_tier"`
		}
		if json.Unmarshal(body, &wire) != nil || wire.Model != c.ActualModel || wire.ServiceTier != "default" {
			return ErrProviderLimit
		}
		return nil
	}
	if c.InputBoundProfile != OpenAIEmbeddingsV1 || preparedURL != "https://api.openai.com/v1/embeddings" {
		return ErrProviderLimit
	}
	if err := ValidatePreparedRequest(body, "openai", true); err != nil {
		return err
	}
	var request struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || request.Model != c.ActualModel {
		return ErrProviderLimit
	}
	if _, single := strictString(request.Input); !single {
		var batch []string
		if json.Unmarshal(request.Input, &batch) != nil || len(batch) > 2048 {
			return ErrProviderLimit
		}
	}
	// No character/token estimate: occupy the entire documented request cap,
	// even for one short input. Per-input token rejection remains upstream.
	return nil
}
