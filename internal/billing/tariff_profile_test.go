package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"minigateway/internal/model"
)

const tariffTestKey = "sk-tariff-fixture-only"

func testKeyDigest(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func tariffTestHeader() http.Header { return http.Header{"Authorization": {"Bearer " + tariffTestKey}} }

func withTestTariff(c ProviderCostContract) ProviderCostContract {
	c.TariffProfile, c.Currency = OpenAIStandardUSD20261017, "USD"
	c.CredentialBindings = map[string]string{"1": testKeyDigest(tariffTestKey)}
	return c
}

func TestTariffProfilesRequireStandardCurrencyRatesAndBindings(t *testing.T) {
	for _, tc := range []struct{ model, input, output, profile, operation string }{
		{"gpt-4o-mini-2024-07-18", "0.00015", "0.0006", OpenAIChatV1, "chat"},
		{"text-embedding-3-small", "0.00002", "0", OpenAIEmbeddingsV1, "embeddings"},
		{"text-embedding-3-large", "0.00013", "0", OpenAIEmbeddingsV1, "embeddings"},
		{"text-embedding-ada-002", "0.0001", "0", OpenAIEmbeddingsV1, "embeddings"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c := withTestTariff(embeddingProfileContract())
			c.ActualModel, c.InputPricePer1K, c.OutputPricePer1K = tc.model, tc.input, tc.output
			c.InputBoundProfile, c.Operation = tc.profile, tc.operation
			if tc.operation == "chat" {
				c.OutputLimitField, c.MaxOutputTokens = "max_completion_tokens", 32
			}
			if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			for name, edit := range map[string]func(*ProviderCostContract){
				"unknown profile":  func(c *ProviderCostContract) { c.TariffProfile = "future" },
				"missing profile":  func(c *ProviderCostContract) { c.TariffProfile = "" },
				"missing currency": func(c *ProviderCostContract) { c.Currency = "" },
				"wrong currency":   func(c *ProviderCostContract) { c.Currency = "CNY" },
				"zero price":       func(c *ProviderCostContract) { c.InputPricePer1K = "0" },
				"below input floor": func(c *ProviderCostContract) {
					units, _ := exactDecimalUnits(c.InputPricePer1K)
					c.InputPricePer1K = fmt.Sprintf("0.%09d", units-1)
				},
				"no bindings": func(c *ProviderCostContract) { c.CredentialBindings = nil },
				"zero ID": func(c *ProviderCostContract) {
					c.CredentialBindings = map[string]string{"0": testKeyDigest(tariffTestKey)}
				},
				"noncanonical ID": func(c *ProviderCostContract) {
					c.CredentialBindings = map[string]string{"01": testKeyDigest(tariffTestKey)}
				},
				"invalid fingerprint": func(c *ProviderCostContract) {
					c.CredentialBindings = map[string]string{"1": "plaintext-is-not-a-digest"}
				},
				"uppercase fingerprint": func(c *ProviderCostContract) {
					c.CredentialBindings = map[string]string{"1": strings.ToUpper(testKeyDigest(tariffTestKey))}
				},
			} {
				t.Run(name, func(t *testing.T) {
					changed := c
					edit(&changed)
					if _, err := compileProviderCosts("1", []ProviderCostContract{changed}); err == nil {
						t.Fatal("unsafe tariff accepted")
					}
				})
			}
			if tc.operation == "chat" {
				c.OutputPricePer1K = "0.000599999"
				if _, err := compileProviderCosts("1", []ProviderCostContract{c}); err == nil {
					t.Fatal("output tariff below standard floor")
				}
			}
		})
	}
}

func TestTariffAdmissionChecksWireCredentialAndPinsImmutableBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProviderCostContract, *model.BillingAttempt, http.Header)
		want error
	}{
		{"matching", func(*ProviderCostContract, *model.BillingAttempt, http.Header) {}, nil},
		{"no tariff", func(c *ProviderCostContract, _ *model.BillingAttempt, _ http.Header) {
			c.TariffProfile, c.Currency, c.CredentialBindings = "", "", nil
		}, ErrTariffUnproven},
		{"new ID", func(_ *ProviderCostContract, a *model.BillingAttempt, _ http.Header) { a.CredentialID = 2 }, ErrCredentialTariff},
		{"missing ID", func(_ *ProviderCostContract, a *model.BillingAttempt, _ http.Header) { a.CredentialID = 0 }, ErrCredentialTariff},
		{"rotated key", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) {
			h.Set("Authorization", "Bearer sk-rotated-fixture-only")
		}, ErrCredentialTariff},
		{"missing auth", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) { h.Del("Authorization") }, ErrCredentialTariff},
		{"duplicate auth", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) {
			h.Add("Authorization", "Bearer "+tariffTestKey)
		}, ErrCredentialTariff},
		{"mixed-case duplicate", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) {
			h["authorization"] = []string{"Bearer " + tariffTestKey}
		}, ErrCredentialTariff},
		{"alternate project", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) {
			h.Set("OpenAI-Project", "other-project")
		}, ErrCredentialTariff},
		{"alternate organization", func(_ *ProviderCostContract, _ *model.BillingAttempt, h http.Header) {
			h.Set("OpenAI-Organization", "other-org")
		}, ErrCredentialTariff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, user, key := strictFixture(t, 100, 100)
			if err := e.EnableStrict("46", time.Minute); err != nil {
				t.Fatal(err)
			}
			c := withTestTariff(embeddingProfileContract())
			a := model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: c.ActualModel}
			header := tariffTestHeader()
			tc.edit(&c, &a, header)
			if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
				t.Fatal(err)
			}
			if c.CredentialBindings != nil {
				c.CredentialBindings["1"] = testKeyDigest("sk-rotated-fixture-only")
			}
			ctx := context.Background()
			pre, err := e.Reserve(ctx, key, user, "m")
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"model":"text-embedding-3-small","input":["one","two"]}`)
			err = e.StartPreparedAttempt(ctx, pre, a, providerTestTarget(c), StrictRequest{}, c.BaseURL+"/v1/embeddings", body, header)
			if !errors.Is(err, tc.want) {
				t.Fatalf("credential admission: %v, want %v", err, tc.want)
			}
			detail, err := e.ReservationDetail(ctx, pre.BillingID())
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != nil {
				if len(detail.Attempts) != 0 || detail.Reservation.ProviderHeldUnits != "0" || detail.Reservation.Reason != providerRefusalReason(tc.want) {
					t.Fatalf("refusal consumed cost or lost reason: %+v", detail)
				}
				return
			}
			if len(detail.Attempts) != 1 || detail.Reservation.ProviderHeldUnits != "6000000" {
				t.Fatalf("missing envelope: %+v", detail)
			}
			var snapshot providerAttemptSnapshot
			if err := json.Unmarshal([]byte(detail.Attempts[0].ProviderContractSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Contract.Currency != "USD" || snapshot.Contract.TariffProfile != OpenAIStandardUSD20261017 || snapshot.Contract.CredentialBindings["1"] != testKeyDigest(tariffTestKey) || strings.Contains(detail.Attempts[0].ProviderContractSnapshot, tariffTestKey) {
				t.Fatalf("snapshot lost immutable tariff or disclosed key: %+v", snapshot)
			}
		})
	}
}

func TestTariffRefusalRecoveryPreservesUnknownExecution(t *testing.T) {
	for _, refusal := range []error{ErrTariffUnproven, ErrCredentialTariff} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", providerRefusalReason(refusal), unknown), func(t *testing.T) {
				e, st, user, key := strictFixture(t, 100, 100)
				if err := e.EnableStrict("46", time.Minute); err != nil {
					t.Fatal(err)
				}
				c := embeddingProfileContract()
				header := tariffTestHeader()
				if refusal == ErrCredentialTariff {
					c = withTestTariff(c)
					header.Set("Authorization", "Bearer sk-unregistered-fixture-only")
				}
				if err := e.ConfigureProviderCosts("1", []ProviderCostContract{c}); err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				pre, err := e.Reserve(ctx, key, user, "m")
				if err != nil {
					t.Fatal(err)
				}
				a := model.BillingAttempt{Number: 1, ChannelID: 1, CredentialID: 1, ActualModel: c.ActualModel}
				if unknown {
					if err := e.startDeclaredTestAttempt(ctx, pre, a, providerTestTarget(c), StrictRequest{}); err != nil {
						t.Fatal(err)
					}
					a.Number++
				}
				err = e.StartPreparedAttempt(ctx, pre, a, providerTestTarget(c), StrictRequest{}, c.BaseURL+"/v1/embeddings", []byte(`{"model":"text-embedding-3-small","input":"hello"}`), header)
				if !errors.Is(err, refusal) {
					t.Fatalf("refusal: %v", err)
				}
				restarted := New(st.DB(), e.prices)
				if err := restarted.EnableStrict("46", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := restarted.RecoverExpired(ctx, time.Now().Add(2*time.Minute)); err != nil {
					t.Fatal(err)
				}
				detail, err := restarted.ReservationDetail(ctx, pre.BillingID())
				if err != nil {
					t.Fatal(err)
				}
				state, reason, held, attempts, reserved := Settled, providerRefusalReason(refusal), "0", 0, int64(0)
				if unknown {
					state, reason, held, attempts, reserved = Pending, "execution_or_usage_unknown:"+reason, "6000000", 1, 46_000_000_000
				}
				if detail.Reservation.State != state || detail.Reservation.Reason != reason || detail.Reservation.ProviderHeldUnits != held || len(detail.Attempts) != attempts || detail.Reservation.ChargeUnits != "0" {
					t.Fatalf("recovery lost tariff refusal facts: %+v", detail)
				}
				for _, owner := range []struct {
					kind string
					id   uint
				}{{"user", user}, {"key", key}} {
					balance := readBalance(t, st, owner.kind, owner.id)
					if balance.ReservedUnits != reserved || balance.UsedUnits != 0 {
						t.Fatalf("recovery balance: %+v", balance)
					}
				}
			})
		}
	}
}
