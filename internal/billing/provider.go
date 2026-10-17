package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"minigateway/internal/model"
)

var (
	ErrProviderContract = errors.New("no matching provider cost contract")
	ErrInputUnproven    = errors.New("provider input bound requires a verified profile")
	ErrTariffUnproven   = errors.New("provider tariff requires a registered currency and rate profile")
	ErrCredentialTariff = errors.New("provider credential is not covered by the tariff contract")
	ErrProviderBudget   = errors.New("provider request budget exhausted")
	ErrProviderLimit    = errors.New("request exceeds the provider cost contract")
	ErrFeeBound         = errors.New("request fee bound exceeds the reserved user fee ceiling")
)

// ProviderCostContract is an operator-attested upper tariff, NOT a selling
// price. InputTokenCeiling covers the whole attempt (including embedding
// batches and rejected requests); prices must cover all billable token classes.
// Evidence must explain the provider-enforced ceilings and tariff assumptions.
// There are deliberately no model aliases, wildcards or inferred defaults.
type ProviderCostContract struct {
	ChannelID          uint              `json:"channel_id"`
	BaseURL            string            `json:"base_url"`
	Protocol           string            `json:"protocol"`
	ActualModel        string            `json:"actual_model"`
	Operation          string            `json:"operation"`
	OutputLimitField   string            `json:"output_limit_field"`
	InputTokenCeiling  int64             `json:"input_token_ceiling"`
	InputBoundProfile  string            `json:"input_bound_profile,omitempty"`
	MaxOutputTokens    int64             `json:"max_output_tokens"`
	InputPricePer1K    string            `json:"input_price_per_1k"`
	OutputPricePer1K   string            `json:"output_price_per_1k"`
	Evidence           string            `json:"evidence"`
	TariffProfile      string            `json:"tariff_profile,omitempty"`
	Currency           string            `json:"currency,omitempty"`
	CredentialBindings map[string]string `json:"credential_bindings,omitempty"`
}

type ProviderTarget struct {
	ChannelID   uint
	BaseURL     string
	Protocol    string
	ActualModel string
	Operation   string
}

type providerKey struct {
	ProviderTarget
	field string
}

type providerContract struct {
	config        ProviderCostContract
	input, output int64
}

type providerAttemptSnapshot struct {
	Format              int                  `json:"format"`
	UnitScale           int64                `json:"unit_scale"`
	Contract            ProviderCostContract `json:"contract"`
	OutputLimit         int64                `json:"output_limit"`
	SellingPriceVersion string               `json:"selling_price_version,omitempty"`
	SellingModel        string               `json:"selling_model,omitempty"`
	SellingGroup        string               `json:"selling_group,omitempty"`
	SellingBoundUnits   string               `json:"selling_bound_units,omitempty"`
}

// ProviderCosts is immutable after compilation and is pinned at reservation.
type ProviderCosts struct {
	budget    int64
	contracts map[providerKey]providerContract
}

var monetaryDecimal = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,9})?$`)

func exactDecimalUnits(value string) (int64, error) {
	if !monetaryDecimal.MatchString(value) {
		return 0, errors.New("money requires a nonnegative decimal string with at most nine places")
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok {
		return 0, errors.New("invalid money")
	}
	r.Mul(r, new(big.Rat).SetInt64(UnitScale))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, errors.New("money exceeds fixed-point range")
	}
	return r.Num().Int64(), nil
}

func compileProviderCosts(budget string, configs []ProviderCostContract) (*ProviderCosts, error) {
	units, err := exactDecimalUnits(budget)
	if err != nil || units <= 0 || len(configs) == 0 {
		return nil, errors.New("provider costs require a positive request budget and explicit contracts")
	}
	policy := &ProviderCosts{budget: units, contracts: make(map[providerKey]providerContract)}
	for _, c := range configs {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("provider contract requires an absolute HTTP base URL without credentials, query or fragment")
		}
		if c.ChannelID == 0 || strings.TrimSpace(c.ActualModel) == "" ||
			strings.Contains(c.ActualModel, "*") || strings.TrimSpace(c.Evidence) == "" || c.InputTokenCeiling <= 0 {
			return nil, errors.New("provider contract requires an exact channel/model, evidence and whole-attempt input ceiling")
		}
		switch c.Protocol {
		case "openai", "anthropic", "gemini":
		default:
			return nil, errors.New("unsupported provider contract protocol")
		}
		switch c.Operation {
		case "chat":
			if c.MaxOutputTokens <= 0 || (c.OutputLimitField != "max_tokens" && c.OutputLimitField != "max_completion_tokens") ||
				(c.Protocol != "openai" && c.OutputLimitField != "max_tokens") {
				return nil, errors.New("provider chat contract requires a supported, explicit total billable output limit")
			}
		case "embeddings":
			if c.Protocol != "openai" || c.MaxOutputTokens != 0 || c.OutputLimitField != "" {
				return nil, errors.New("provider embedding contract requires OpenAI-compatible input-only billing")
			}
		default:
			return nil, errors.New("unsupported provider contract operation")
		}
		if err := validateInputBoundProfile(c); err != nil {
			return nil, err
		}
		in, err := exactDecimalUnits(c.InputPricePer1K)
		if err != nil {
			return nil, fmt.Errorf("provider input tariff: %w", err)
		}
		out, err := exactDecimalUnits(c.OutputPricePer1K)
		if err != nil || (c.Operation == "embeddings" && out != 0) {
			return nil, errors.New("invalid provider output tariff")
		}
		contract := providerContract{config: c, input: in, output: out}
		if err := validateTariffProfile(contract); err != nil {
			return nil, err
		}
		if c.CredentialBindings != nil {
			contract.config.CredentialBindings = make(map[string]string, len(c.CredentialBindings))
			for id, fingerprint := range c.CredentialBindings {
				contract.config.CredentialBindings[id] = fingerprint
			}
		}
		// Validate the entire declared envelope, not just the first request.
		if _, err := contract.bound(c.MaxOutputTokens); err != nil {
			return nil, err
		}
		key := providerKey{ProviderTarget{c.ChannelID, c.BaseURL, c.Protocol, c.ActualModel, c.Operation}, c.OutputLimitField}
		if _, exists := policy.contracts[key]; exists {
			return nil, errors.New("duplicate provider cost contract")
		}
		policy.contracts[key] = contract
	}
	return policy, nil
}

func (c providerContract) bound(output int64) (int64, error) {
	cost := new(big.Int).Mul(big.NewInt(c.config.InputTokenCeiling), big.NewInt(c.input))
	cost.Add(cost, new(big.Int).Mul(big.NewInt(output), big.NewInt(c.output)))
	cost.Add(cost, big.NewInt(999)).Quo(cost, big.NewInt(1000))
	if !cost.IsInt64() {
		return 0, errors.New("provider cost bound exceeds fixed-point range")
	}
	return cost.Int64(), nil
}

// ConfigureProviderCosts is a one-time startup operation. Ledger recovery can
// run without a policy, but the gateway cannot start uncosted strict attempts.
func (e *Engine) ConfigureProviderCosts(budget string, configs []ProviderCostContract) error {
	policy, err := compileProviderCosts(budget, configs)
	if err != nil {
		return err
	}
	if !e.providerCosts.CompareAndSwap(nil, policy) {
		return errors.New("provider costs are immutable; restart to change the policy")
	}
	return nil
}

type providerBound struct {
	units             int64
	feeUnits          int64
	version, snapshot string
}

// Deprecated: use StartPreparedAttempt. Metadata alone cannot prove the bounds
// of the request actually sent, so this entry cannot admit an execution.
func (e *Engine) StartCostedAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, target ProviderTarget, request StrictRequest) error {
	return e.startCostedAttempt(ctx, pre, attempt, target, request, "", nil, nil)
}

// StartPreparedAttempt verifies profiles against the payload and destination
// that the executor will actually use, before occupying the attempt envelope.
func (e *Engine) StartPreparedAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, target ProviderTarget, request StrictRequest, preparedURL string, preparedBody []byte, preparedHeader http.Header) error {
	return e.startCostedAttempt(ctx, pre, attempt, target, request, preparedURL, preparedBody, preparedHeader)
}

func (e *Engine) startCostedAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, target ProviderTarget, request StrictRequest, preparedURL string, preparedBody []byte, preparedHeader http.Header) (err error) {
	defer func() {
		err = e.RecordCostRefusal(ctx, pre, err)
	}()
	contract, err := e.lookupCostContract(pre, attempt, target, request)
	if err != nil {
		return err
	}
	if contract.config.InputBoundProfile == "" {
		return ErrInputUnproven
	}
	if preparedBody == nil {
		return ErrProviderContract
	}
	wire, err := ParseStrictRequest(preparedBody, target.Operation == "embeddings")
	if err != nil || wire != request {
		return ErrProviderLimit
	}
	if err := checkProfileRequest(contract.config, preparedURL, preparedBody); err != nil {
		return err
	}
	if err := checkTariffCredential(contract.config, attempt.CredentialID, preparedHeader); err != nil {
		return err
	}
	return e.startContractAttempt(ctx, pre, attempt, contract, request)
}

func (e *Engine) lookupCostContract(pre *PreDeduct, attempt model.BillingAttempt, target ProviderTarget, request StrictRequest) (providerContract, error) {
	if !e.StrictReady() || pre == nil || pre.reservation == nil || pre.reservation.providerCosts == nil ||
		target.ChannelID != attempt.ChannelID || target.ActualModel != attempt.ActualModel {
		return providerContract{}, ErrProviderContract
	}
	contract, ok := pre.reservation.providerCosts.contracts[providerKey{target, request.OutputLimitField}]
	if !ok {
		return providerContract{}, ErrProviderContract
	}
	return contract, nil
}

func (e *Engine) startContractAttempt(ctx context.Context, pre *PreDeduct, attempt model.BillingAttempt, contract providerContract, request StrictRequest) error {
	if request.OutputLimit < 0 || int64(request.OutputLimit) > contract.config.MaxOutputTokens ||
		(contract.config.Operation == "chat" && request.OutputLimit == 0) ||
		(contract.config.Operation == "embeddings" && request.OutputLimit != 0) {
		return ErrProviderLimit
	}
	units, err := contract.bound(int64(request.OutputLimit))
	if err != nil {
		return err
	}
	sellingModel := snapshotPriceModel(pre.prices, attempt.ActualModel, pre.reservation.origin)
	feeUnits, err := pre.prices.costUnits(contract.config.InputTokenCeiling, int64(request.OutputLimit), sellingModel, attempt.Group)
	if err != nil {
		return ErrFeeBound
	}
	payload, err := json.Marshal(providerAttemptSnapshot{
		Format: 2, UnitScale: UnitScale, Contract: contract.config, OutputLimit: int64(request.OutputLimit),
		SellingPriceVersion: pre.prices.Version(), SellingModel: sellingModel, SellingGroup: attempt.Group,
		SellingBoundUnits: fmt.Sprintf("%d", feeUnits),
	})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	return e.startAttempt(ctx, pre, attempt, &providerBound{units: units, feeUnits: feeUnits, version: hex.EncodeToString(hash[:]), snapshot: string(payload)})
}

// RecordCostRefusal persists only known admission codes, never raw errors.
// It records no attempt and does not change holds or accounting state.
func (e *Engine) RecordCostRefusal(ctx context.Context, pre *PreDeduct, cause error) error {
	reason := providerRefusalReason(cause)
	if reason == "" {
		return cause
	}
	if !e.StrictReady() || pre == nil || pre.reservation == nil {
		return ErrStrictUnavailable
	}
	// Diagnostics survive client cancellation without extending execution.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	res := e.strict.db.WithContext(recordCtx).Model(&model.BillingReservation{}).
		Where("id=? AND state IN ?", pre.BillingID(), []string{Reserved, Active}).Update("reason", reason)
	if res.Error != nil || res.RowsAffected != 1 {
		return ErrStrictUnavailable
	}
	return cause
}

func providerRefusalReason(err error) string {
	switch {
	case errors.Is(err, ErrProviderContract):
		return "provider_cost_contract_missing"
	case errors.Is(err, ErrInputUnproven):
		return "provider_input_unproven"
	case errors.Is(err, ErrTariffUnproven):
		return "provider_tariff_unproven"
	case errors.Is(err, ErrCredentialTariff):
		return "provider_credential_unproven"
	case errors.Is(err, ErrProviderBudget):
		return "provider_budget_exhausted"
	case errors.Is(err, ErrProviderLimit):
		return "provider_cost_limit"
	case errors.Is(err, ErrFeeBound):
		return "billing_fee_limit"
	case errors.Is(err, ErrUnpricedDimension):
		return "unsupported_billing_dimension"
	default:
		return ""
	}
}

func persistedProviderRefusal(reason string) string {
	reason = strings.TrimPrefix(reason, "execution_or_usage_unknown:")
	switch reason {
	case "provider_cost_contract_missing", "provider_input_unproven", "provider_tariff_unproven", "provider_credential_unproven", "provider_budget_exhausted", "provider_cost_limit", "billing_fee_limit", "unsupported_billing_dimension":
		return reason
	default:
		return ""
	}
}
