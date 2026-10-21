package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

const operatorRequest = `{"model":"m","input":"hello","max_output_tokens":32,"store":false,"stream":true}`

func operatorContract() ProviderCostContract {
	return ProviderCostContract{ChannelID: 1, BaseURL: "https://relay.example", Protocol: "openai", ActualModel: "m", Operation: "responses", OutputLimitField: "max_output_tokens",
		InputTokenCeiling: 2048, InputBoundProfile: OperatorResponsesV1, MaxOutputTokens: 32, InputPricePer1K: "0.001", OutputPricePer1K: "0.002",
		Evidence:      "Operator asserts whole-request input bound, all token classes and additional fees for at most 32 control RPCs; not a remote invoice certification.",
		TariffProfile: OperatorDeclaredTokensV1, Currency: "USD", CredentialBindings: map[string]string{"1": testKeyDigest(tariffTestKey)},
		PolicyVersion: "fixture-contract-v1", PolicyExpiresAt: time.Now().Add(time.Hour).Unix(), MaxRequestBytes: 1024, AdditionalCostPerAttempt: "0.0001"}
}

func TestOperatorPolicyRequiresCompleteBoundDeclaration(t *testing.T) {
	for name, edit := range map[string]func(*ProviderCostContract){
		"no version":                    func(c *ProviderCostContract) { c.PolicyVersion = "" },
		"version whitespace":            func(c *ProviderCostContract) { c.PolicyVersion = " contract" },
		"version too long":              func(c *ProviderCostContract) { c.PolicyVersion = strings.Repeat("x", 129) },
		"expired":                       func(c *ProviderCostContract) { c.PolicyExpiresAt = time.Now().Add(-time.Second).Unix() },
		"no expiry":                     func(c *ProviderCostContract) { c.PolicyExpiresAt = 0 },
		"no bytes":                      func(c *ProviderCostContract) { c.MaxRequestBytes = 0 },
		"oversize bytes":                func(c *ProviderCostContract) { c.MaxRequestBytes = 1<<20 + 1; c.InputTokenCeiling = 2 << 20 },
		"bytes outside input envelope":  func(c *ProviderCostContract) { c.MaxRequestBytes = 2049 },
		"no additional fee declaration": func(c *ProviderCostContract) { c.AdditionalCostPerAttempt = "" },
		"bad additional fee":            func(c *ProviderCostContract) { c.AdditionalCostPerAttempt = "1e-3" },
		"negative additional fee":       func(c *ProviderCostContract) { c.AdditionalCostPerAttempt = "-1" },
		"additional overflow":           func(c *ProviderCostContract) { c.AdditionalCostPerAttempt = "9223372036.854775807" },
		"plain remote HTTP":             func(c *ProviderCostContract) { c.BaseURL = "http://relay.example" },
		"URL credential":                func(c *ProviderCostContract) { c.BaseURL = "https://secret@relay.example" },
		"URL query":                     func(c *ProviderCostContract) { c.BaseURL += "?key=secret" },
		"URL fragment":                  func(c *ProviderCostContract) { c.BaseURL += "#fragment" },
		"unknown profile":               func(c *ProviderCostContract) { c.InputBoundProfile = "future" },
		"wrong operation":               func(c *ProviderCostContract) { c.Operation = "chat" },
		"wrong output field":            func(c *ProviderCostContract) { c.OutputLimitField = "max_tokens" },
		"wrong currency":                func(c *ProviderCostContract) { c.Currency = "US$" },
		"no bindings":                   func(c *ProviderCostContract) { c.CredentialBindings = nil },
		"bad binding":                   func(c *ProviderCostContract) { c.CredentialBindings = map[string]string{"1": "plaintext"} },
		"missing input tariff":          func(c *ProviderCostContract) { c.InputPricePer1K = "0" },
		"missing output tariff":         func(c *ProviderCostContract) { c.OutputPricePer1K = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			c := operatorContract()
			edit(&c)
			if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err == nil {
				t.Fatal("unsafe operator declaration accepted")
			}
		})
	}
	for _, base := range []string{"https://relay.example", "http://127.0.0.1:1234", "http://[::1]:1234"} {
		c := operatorContract()
		c.BaseURL = base
		policy, err := compileProviderCosts("1", []ProviderCostContract{c})
		if err != nil {
			t.Fatal(err)
		}
		contract := policy.contracts[providerKey{providerTestTarget(c), c.OutputLimitField}]
		if cost, err := contract.bound(32); err != nil || cost != 2_212_000 {
			t.Fatalf("full attempt envelope %d %v", cost, err)
		}
	}
	c := withTestTariff(embeddingProfileContract())
	c.AdditionalCostPerAttempt = "0"
	if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err == nil {
		t.Fatal("operator fields leaked into historical tariff")
	}
}

func TestResponsesStrictRequestClosedTextAndFunctionSurface(t *testing.T) {
	tool := `{"type":"function","name":"weather","parameters":{"type":"object"},"strict":true}`
	call := `{"type":"function_call","id":"fc_1","status":"completed","call_id":"c1","name":"weather","arguments":"{\"city\":\"Shanghai\"}"}`
	result := `{"type":"function_call_output","call_id":"c1","output":"22 C"}`
	message := `{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}`
	for _, body := range []string{operatorRequest,
		`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":32}`,
		`{"model":"m","input":[` + call + `,` + result + `,` + message + `],"max_output_tokens":32,"tools":[` + tool + `],"tool_choice":{"type":"function","name":"weather"},"parallel_tool_calls":true}`,
		`{"model":"m","input":"","instructions":"","tools":[],"tool_choice":"none","max_output_tokens":1,"background":true,"stream":false,"store":true}`,
		`{"type":"response.create","model":"m","input":"hi","max_output_tokens":32,"temperature":0.3,"top_p":1,"service_tier":"default","truncation":"disabled"}`,
	} {
		if request, err := ParseResponsesStrictRequest([]byte(body)); err != nil || request.OutputLimitField != "max_output_tokens" {
			t.Fatalf("supported request rejected: %s %v", body, err)
		}
	}
	invalid := []string{
		`null`, `[]`, `{"model":"m","input":"hi"}`, `{"model":"m","input":"hi","max_output_tokens":null}`,
		`{"model":"m","input":"hi","max_output_tokens":0}`, `{"model":"m","input":"hi","max_output_tokens":1.2}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"max_output_tokens":1}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"previous_response_id":"opaque"}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"reasoning":{"effort":"low"}}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"conversation":"opaque"}`,
		`{"model":"m","input":null,"max_output_tokens":32}`,
		`{"model":"m","input":[],"max_output_tokens":32}`,
		`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://a"}]}],"max_output_tokens":32}`,
		`{"model":"m","input":[` + call + `],"max_output_tokens":32}`,
		`{"model":"m","input":[` + result + `],"max_output_tokens":32}`,
		`{"model":"m","input":[` + call + `,` + call + `,` + result + `],"max_output_tokens":32}`,
		`{"model":"m","input":[` + call + `,` + result + `,` + result + `],"max_output_tokens":32}`,
		`{"model":"m","input":[` + strings.Replace(call, `\"city\":\"Shanghai\"`, `\"city\":1,\"city\":2`, 1) + `,` + result + `],"max_output_tokens":32}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tools":[{"type":"web_search"}]}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tools":null}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tools":[` + tool + `,` + tool + `]}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tools":[` + strings.Replace(tool, `"object"`, `"object","type":"object"`, 1) + `]}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tools":[],"tool_choice":"required"}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"tool_choice":{"type":"function","name":"absent"}}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"parallel_tool_calls":null}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"temperature":null}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"temperature":3}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"service_tier":"priority"}`,
		`{"model":"m","input":"hi","max_output_tokens":32,"type":"response.cancel"}`,
	}
	for _, body := range invalid {
		if _, err := ParseResponsesStrictRequest([]byte(body)); err == nil {
			t.Fatalf("unsupported billing surface accepted: %s", body)
		}
	}
	for _, count := range []int{64<<10 + 1, 1<<20 + 1} {
		body := `{"model":"m","input":` + strconv.Quote(strings.Repeat("x", count)) + `,"max_output_tokens":32}`
		if _, err := ParseResponsesStrictRequest([]byte(body)); err == nil {
			t.Fatal("unbounded input accepted")
		}
	}
}

func TestOperatorWireAdmissionBindsActualTargetAndCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProviderCostContract, *string, *[]byte, http.Header)
		want error
	}{
		{"matching", func(*ProviderCostContract, *string, *[]byte, http.Header) {}, nil},
		{"wrong endpoint", func(_ *ProviderCostContract, u *string, _ *[]byte, _ http.Header) { *u += "/other" }, ErrProviderLimit},
		{"query", func(_ *ProviderCostContract, u *string, _ *[]byte, _ http.Header) { *u += "?api_key=x" }, ErrProviderLimit},
		{"wrong model", func(_ *ProviderCostContract, _ *string, b *[]byte, _ http.Header) {
			*b = []byte(strings.Replace(string(*b), `"m"`, `"other"`, 1))
		}, ErrProviderLimit},
		{"over body cap", func(c *ProviderCostContract, _ *string, _ *[]byte, _ http.Header) { c.MaxRequestBytes = 1 }, ErrProviderLimit},
		{"rotated key", func(_ *ProviderCostContract, _ *string, _ *[]byte, h http.Header) {
			h.Set("Authorization", "Bearer changed")
		}, ErrCredentialTariff},
		{"other project", func(_ *ProviderCostContract, _ *string, _ *[]byte, h http.Header) { h.Set("OpenAI-Project", "other") }, ErrCredentialTariff},
		{"wrong beta", func(_ *ProviderCostContract, _ *string, _ *[]byte, h http.Header) { h.Set("OpenAI-Beta", "unknown") }, ErrCredentialTariff},
		{"WS", func(_ *ProviderCostContract, u *string, b *[]byte, h http.Header) {
			*u = strings.Replace(*u, "https://", "wss://", 1)
			*b = []byte(strings.Replace(string(*b), `{"model"`, `{"type":"response.create","model"`, 1))
			h.Set("OpenAI-Beta", "responses_websockets=2025-06-01")
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, st, user, key := strictFixture(t, 100, 100)
			if err := e.EnableStrict("1", time.Minute); err != nil {
				t.Fatal(err)
			}
			c := operatorContract()
			u := c.BaseURL + "/v1/responses"
			body := []byte(operatorRequest)
			header := tariffTestHeader()
			tc.edit(&c, &u, &body, header)
			if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			pre, err := e.Reserve(context.Background(), key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			err = e.StartPreparedAttempt(context.Background(), pre, model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: "m"}, providerTestTarget(c), StrictRequest{32, "max_output_tokens"}, u, body, header)
			if !errors.Is(err, tc.want) {
				t.Fatalf("admission %v want %v", err, tc.want)
			}
			var row model.BillingReservation
			if err := st.DB().First(&row, "id=?", pre.BillingID()).Error; err != nil {
				t.Fatal(err)
			}
			if row.ProviderPolicyVersion == "" {
				t.Fatal("admission policy not pinned before dispatch")
			}
			var attempts int64
			st.DB().Model(&model.BillingAttempt{}).Where("reservation_id=?", pre.BillingID()).Count(&attempts)
			if tc.want == nil && attempts != 1 || tc.want != nil && attempts != 0 {
				t.Fatalf("unexpected attempts %d", attempts)
			}
		})
	}
}

func TestOperatorPolicyPinsFailureEnvelopeAndUnknownHold(t *testing.T) {
	e, st, user, key := strictFixture(t, 100, 100)
	if err := e.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	c := operatorContract()
	if err := e.ConfigureProviderCosts("0.004424", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	c.CredentialBindings["1"] = testKeyDigest("changed-after-config")
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		err = e.StartPreparedAttempt(ctx, pre, model.BillingAttempt{Number: n, ChannelID: 1, CredentialID: 1, ActualModel: "m"}, providerTestTarget(c), StrictRequest{32, "max_output_tokens"}, c.BaseURL+"/v1/responses", []byte(operatorRequest), tariffTestHeader())
		if n == 3 {
			if !errors.Is(err, ErrProviderBudget) {
				t.Fatalf("third attempt exceeded aggregate declared budget: %v", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := e.EndAttempt(ctx, pre, n, 503, "upstream_error", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, state, err := e.Finish(ctx, pre, nil, "m", "", false); err != nil || state != Pending {
		t.Fatalf("unknown did not remain pending: %s %v", state, err)
	}
	detail, err := e.ReservationDetail(ctx, pre.BillingID())
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 || detail.Reservation.ProviderHeldUnits != "4424000" {
		t.Fatalf("failure envelope lost: %+v", detail)
	}
	var snapshot providerAttemptSnapshot
	if json.Unmarshal([]byte(detail.Attempts[0].ProviderContractSnapshot), &snapshot) != nil || snapshot.Contract.PolicyVersion != "fixture-contract-v1" || snapshot.Contract.AdditionalCostPerAttempt != "0.0001" || snapshot.Contract.CredentialBindings["1"] != testKeyDigest(tariffTestKey) {
		t.Fatal("immutable contract snapshot lost its exact declaration")
	}
	var balances []model.BillingBalance
	if err := st.DB().Where("owner_id IN ?", []uint{user, key}).Find(&balances).Error; err != nil {
		t.Fatal(err)
	}
	if len(balances) != 2 {
		t.Fatalf("missing dual balances %+v", balances)
	}
	for _, b := range balances {
		if b.ReservedUnits != UnitScale || b.UsedUnits != 0 {
			t.Fatalf("unknown released hold %+v", b)
		}
	}
	c = operatorContract()
	c.PolicyExpiresAt = time.Now().Unix() - 1
	if err := checkOperatorRequest(c, c.BaseURL+"/v1/responses", []byte(operatorRequest)); !errors.Is(err, ErrTariffUnproven) {
		t.Fatal("expired declaration admitted")
	}
}

func TestOperatorPolicySettlesKnownZeroExactlyOnce(t *testing.T) {
	e, st, user, key := strictFixture(t, 100, 100)
	if err := e.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	c := operatorContract()
	if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pre, err := e.Reserve(ctx, key, user, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartPreparedAttempt(ctx, pre, model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: "m"}, providerTestTarget(c), StrictRequest{32, "max_output_tokens"}, c.BaseURL+"/v1/responses", []byte(operatorRequest), tariffTestHeader()); err != nil {
		t.Fatal(err)
	}
	zero := &relaymodel.Usage{}
	if err := e.EndAttempt(ctx, pre, 1, 200, "success", zero); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if charge, state, err := e.Finish(ctx, pre, zero, "m", "", true); err != nil || state != Settled || charge != 0 {
			t.Fatalf("zero native usage settlement: %d %s %v", charge, state, err)
		}
	}
	var balances []model.BillingBalance
	st.DB().Find(&balances)
	for _, b := range balances {
		if b.ReservedUnits != 0 || b.UsedUnits != 0 {
			t.Fatalf("zero receipt did not release exactly once: %+v", b)
		}
	}
}

func TestOperatorPolicySupportsIndependentStaticChatAndEmbeddings(t *testing.T) {
	for _, operation := range []string{"chat", "embeddings"} {
		c := operatorContract()
		c.Operation = operation
		var body, endpoint string
		if operation == "chat" {
			c.InputBoundProfile = OperatorChatV1
			c.OutputLimitField = "max_completion_tokens"
			body = `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":32}`
			endpoint = "/v1/chat/completions"
		} else {
			c.InputBoundProfile = OperatorEmbeddingsV1
			c.OutputLimitField = ""
			c.MaxOutputTokens = 0
			c.OutputPricePer1K = "0"
			body = `{"model":"m","input":["hi","world"]}`
			endpoint = "/v1/embeddings"
		}
		if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err != nil {
			t.Fatal(err)
		}
		if err := checkOperatorRequest(c, c.BaseURL+endpoint, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := checkOperatorRequest(c, strings.Replace(c.BaseURL, "https://", "wss://", 1)+endpoint, []byte(body)); !errors.Is(err, ErrProviderLimit) {
			t.Fatal("non Responses operation used native WS")
		}
	}
}

func TestResponsesStrictRequestStreamIDRequiresCreate(t *testing.T) {
	for _, tc := range []struct {
		fields string
		valid  bool
	}{
		{`"type":"response.create","stream_id":"main",`, true},
		{`"stream_id":"main",`, false},
		{`"type":"response.create","stream_id":"other",`, false},
		{`"type":"response.create","stream_id":null,`, false},
		{`"type":"response.cancel","stream_id":"main",`, false},
	} {
		_, err := ParseResponsesStrictRequest([]byte(`{` + tc.fields + `"model":"m","input":"hi","max_output_tokens":32}`))
		if (err == nil) != tc.valid {
			t.Fatalf("fields %s: %v", tc.fields, err)
		}
	}
}

func TestOperatorResponsesWireTransportFields(t *testing.T) {
	c := operatorContract()
	body := []byte(`{"type":"response.create","stream_id":"main","model":"m","input":"hi","max_output_tokens":32}`)
	if err := checkOperatorRequest(c, "wss://relay.example/v1/responses", body); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{string(body), `{"type":"response.create","model":"m","input":"hi","max_output_tokens":32}`} {
		if err := checkOperatorRequest(c, "https://relay.example/v1/responses", []byte(raw)); !errors.Is(err, ErrProviderLimit) {
			t.Fatalf("HTTP accepted WS fields: %v", err)
		}
	}
}
