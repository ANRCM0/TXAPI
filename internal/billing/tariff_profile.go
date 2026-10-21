package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// This dated floor covers standard model-token usage, not invoice surcharges
// or rejected-request billing. Sources and remaining assumptions are documented.
const OpenAIStandardUSD20261017 = "openai_standard_usd_2026_10_17"

func validateTariffProfile(c providerContract) error {
	config := c.config
	if config.TariffProfile == "" {
		if config.Currency != "" || len(config.CredentialBindings) != 0 {
			return errors.New("provider currency and credential bindings require a tariff profile")
		}
		return nil // Historical declarations may load, but cannot execute.
	}
	if config.TariffProfile == OperatorDeclaredTokensV1 {
		if err := validateOperatorPolicy(config); err != nil {
			return err
		}
		if len(config.Currency) != 3 || strings.Trim(config.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" || c.input <= 0 || config.Operation != "embeddings" && c.output <= 0 || config.AdditionalCostPerAttempt == "" {
			return errors.New("operator token policy requires an explicit currency, positive input/output tariffs and additional attempt envelope")
		}
		return validateCredentialBindings(config.CredentialBindings)
	}
	if config.TariffProfile != OpenAIStandardUSD20261017 || config.Currency != "USD" {
		return errors.New("unknown provider tariff profile or unsupported currency")
	}
	if config.BaseURL != "https://api.openai.com" || config.Protocol != "openai" {
		return errors.New("standard USD tariff requires the official OpenAI endpoint")
	}
	var input, output int64
	switch {
	case config.InputBoundProfile == OpenAIChatV1 && config.Operation == "chat" && config.ActualModel == "gpt-4o-mini-2024-07-18":
		input, output = 150_000, 600_000 // 1e-9 USD per 1K tokens.
	case config.InputBoundProfile == OpenAIEmbeddingsV1 && config.Operation == "embeddings":
		switch config.ActualModel {
		case "text-embedding-3-small":
			input = 20_000
		case "text-embedding-3-large":
			input = 130_000
		case "text-embedding-ada-002":
			input = 100_000
		default:
			return errors.New("embedding model has no registered standard tariff")
		}
	default:
		return errors.New("provider input profile and tariff target do not match")
	}
	if c.input < input || c.output < output {
		return errors.New("provider tariff understates the registered standard USD rate")
	}
	return validateCredentialBindings(config.CredentialBindings)
}

func validateCredentialBindings(bindings map[string]string) error {
	if len(bindings) == 0 || len(bindings) > 256 {
		return errors.New("provider tariff requires explicit credential ID and key SHA-256 bindings")
	}
	for id, fingerprint := range bindings {
		value, err := strconv.ParseUint(id, 10, strconv.IntSize)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != id {
			return errors.New("provider credential binding requires a canonical positive ID")
		}
		digest, err := hex.DecodeString(fingerprint)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != fingerprint {
			return errors.New("provider credential binding requires a lowercase SHA-256 digest")
		}
	}
	return nil
}

func checkTariffCredential(c ProviderCostContract, credentialID uint, header http.Header) error {
	if c.TariffProfile == "" {
		return ErrTariffUnproven
	}
	// Check what will be sent, not only the selected credential's metadata.
	// Alternate project/org selectors and extra headers are outside this profile.
	var authorization string
	var beta bool
	for name, values := range header {
		switch strings.ToLower(name) {
		case "authorization":
			if authorization != "" || len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
				return ErrCredentialTariff
			}
			authorization = values[0]
		case "content-type", "accept":
		case "openai-beta":
			if beta || c.Operation != "responses" || c.TariffProfile != OperatorDeclaredTokensV1 || len(values) != 1 || values[0] != "responses_websockets=2025-06-01" {
				return ErrCredentialTariff
			}
			beta = true
		default:
			return ErrCredentialTariff
		}
	}
	key := strings.TrimPrefix(authorization, "Bearer ")
	if credentialID == 0 || key == "" || strings.ContainsAny(key, " \t\r\n") {
		return ErrCredentialTariff
	}
	digest := sha256.Sum256([]byte(key))
	if c.CredentialBindings[strconv.FormatUint(uint64(credentialID), 10)] != hex.EncodeToString(digest[:]) {
		return ErrCredentialTariff
	}
	return nil
}
