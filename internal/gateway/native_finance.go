package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"minigateway/internal/billing"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"minigateway/internal/model"
)

func nativeBillingAllowed(g *Gateway) bool {
	return g != nil && (g.billingMode == billing.ModeDisabled || g.billingMode == billing.ModeStrict && g.strictReady())
}

func nativeWSURL(base string) (string, bool) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Host == "" {
		return "", false
	}
	// Match the registered HTTP Responses codec and exact cost-policy URL.
	endpoint := strings.TrimRight(base, "/") + "/v1/responses"
	switch parsed.Scheme {
	case "https":
		return strings.Replace(endpoint, "https://", "wss://", 1), true
	case "http":
		return strings.Replace(endpoint, "http://", "ws://", 1), true
	default:
		return "", false
	}
}

type nativeFinance struct {
	gateway       *Gateway
	pre           *billing.PreDeduct
	actual, group string
	started       bool
}

func (f *nativeFinance) start(ctx context.Context, ch *channel, credential *model.Credential, body []byte) error {
	endpoint, valid := nativeWSURL(ch.baseURL)
	if !valid {
		return billing.ErrProviderContract
	}
	request, err := billing.ParseResponsesStrictRequest(body)
	if err != nil {
		return err
	}
	// The encoded frame is the authoritative actual model, never an alias.
	var fields struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &fields) != nil || fields.Model == "" {
		return billing.ErrProviderContract
	}
	actual := fields.Model
	target := billing.ProviderTarget{ChannelID: uint(ch.id), BaseURL: ch.baseURL, Protocol: ch.typ, ActualModel: actual, Operation: string(v1.Responses)}
	header := http.Header{"Authorization": {"Bearer " + credential.APIKey}, "OpenAI-Beta": {"responses_websockets=2025-06-01"}}
	if err := f.gateway.strictBilling.StartPreparedAttempt(ctx, f.pre, model.BillingAttempt{Number: 1, ChannelID: uint(ch.id), CredentialID: credential.ID, ActualModel: actual, Group: ch.groupName}, target, request, endpoint, body, header); err != nil {
		return err
	}
	f.started, f.actual, f.group = true, actual, ch.groupName
	return nil
}

func (f *nativeFinance) finish(ctx context.Context, observed websocketExchangeObservation, mayHaveSent bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var usage *v1.Usage
	known := false
	if f.started && !mayHaveSent {
		// A dial or pre-frame failure proves no generation body was sent.
		usage, known = &v1.Usage{}, true
	} else if u := observed.usage; u.Validate() == nil && u.Completeness == v2.Complete && (observed.native.Status == "completed" || observed.native.Status == "failed" || observed.native.Status == "incomplete" || observed.native.Status == "cancelled") {
		c := u.Counts
		if c.InputTokens != nil && c.OutputTokens != nil && c.TotalTokens != nil && *c.InputTokens <= math.MaxInt && *c.OutputTokens <= math.MaxInt && *c.TotalTokens <= math.MaxInt {
			usage = &v1.Usage{PromptTokens: int(*c.InputTokens), CompletionTokens: int(*c.OutputTokens), TotalTokens: int(*c.TotalTokens)}
			known = true
		}
	}
	if f.started {
		status, terminal := 0, "execution_or_usage_unknown"
		if known {
			status, terminal = http.StatusOK, "success"
		}
		if err := f.gateway.strictBilling.EndAttempt(cleanup, f.pre, 1, status, terminal, usage); err != nil {
			return err
		}
	}
	_, state, err := f.gateway.strictBilling.Finish(cleanup, f.pre, usage, f.actual, f.group, known)
	if err == nil && state == "" {
		return errors.New("native settlement state unavailable")
	}
	return err
}
