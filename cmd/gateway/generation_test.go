package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

type generationTransport func(*http.Request) (*http.Response, error)

func (f generationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func runtimeKeyFile(t *testing.T, id string, key, digest byte) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"active_key": id, "keys": map[string]string{id: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{key}, 32))}, "digest_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{digest}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime-keys.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func generationRuntimeConfig(keys string) config.GenerationConfig {
	return config.GenerationConfig{Enabled: true, KeysFile: keys, Workers: 1, PollMilliseconds: 20, LeaseSeconds: 1, DeadlineSeconds: 5,
		RetentionSeconds: 60, MaxOutputBytes: 65536, InstanceRecords: 16, OwnerRecords: 8, InstanceBytes: 2 << 20, OwnerBytes: 1 << 20}
}

func TestGenerationRuntimeRequiresActuallyEncryptedStore(t *testing.T) {
	if runtime, err := startGenerationRuntime(config.GenerationConfig{}, nil, nil, nil); err != nil || runtime != nil {
		t.Fatal("disabled component touched dependencies", err)
	}
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plain.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if runtime, err := startGenerationRuntime(generationRuntimeConfig("unused-key-path"), st, nil, nil); err == nil || runtime != nil {
		t.Fatal("plaintext store enabled durable runtime")
	}
}

func TestGenerationRuntimeAssemblyDispatchesAndJoinsWithRetainedKeyChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	credentialKeys := runtimeKeyFile(t, "credential-1", 11, 12)
	contentKeys := runtimeKeyFile(t, "content-1", 41, 72)
	st, err := store.Open(config.DatabaseConfig{Path: path, CredentialKeysFile: credentialKeys})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "runtime-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateChannel(&store.ChannelInput{Name: "runtime-provider", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"m"}, ModelMapping: map[string]string{"m": "gpt-4o-mini-2024-07-18"}, APIKey: "isolated-runtime-provider-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.00015, OutputPricePer1K: 0.0006}); err != nil {
		t.Fatal(err)
	}
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{{ChannelID: channel.ID, BaseURL: channel.BaseURL, Protocol: "openai", ActualModel: "gpt-4o-mini-2024-07-18", Operation: "chat",
		OutputLimitField: "max_completion_tokens", InputTokenCeiling: 128000, MaxOutputTokens: 4096, InputBoundProfile: billing.OpenAIChatV1, InputPricePer1K: "0.00015", OutputPricePer1K: "0.0006",
		TariffProfile: billing.OpenAIStandardUSD20261017, Currency: "USD", CredentialBindings: map[string]string{strconv.FormatUint(uint64(channel.Credentials[0].ID), 10): access.Hash(channel.Credentials[0].APIKey)}, Evidence: "isolated runtime assembly fixture; not provider acceptance evidence"}}); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	g, err := gateway.New(gateway.Options{Channels: st.Channels(), StrictBilling: engine, BillingMode: billing.ModeStrict, TotalTimeout: 3 * time.Second,
		Client: &http.Client{Transport: generationTransport(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	cfg := generationRuntimeConfig(contentKeys)
	runtime, err := startGenerationRuntime(cfg, st, engine, g)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	binding := access.Binding{UserID: user.ID, TokenID: key.ID}
	receipt, err := runtime.acceptor.Admit(context.Background(), binding, "runtime-test", gateway.Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(5 * time.Second)
	var intent model.GenerationIntent
	for {
		if err := st.DB().First(&intent, "id = ?", receipt.ID).Error; err != nil {
			t.Fatal(err)
		}
		if intent.State == model.IntentSucceeded {
			break
		}
		if time.Now().After(until) {
			t.Fatal("assembled runtime did not finish intent", intent.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal("runtime did not join", err)
	}
	if hits.Load() != 1 {
		t.Fatal("runtime replayed")
	}
	if _, err := runtime.archive.Read(context.Background(), binding, intent.OutputRecordID); err != nil {
		t.Fatal("runtime output unreadable", err)
	}
	cfg.KeysFile = runtimeKeyFile(t, "content-2", 42, 72)
	if bad, err := startGenerationRuntime(cfg, st, engine, g); err == nil {
		_ = bad.Close(context.Background())
		t.Fatal("runtime started without retained content keys")
	}
}
