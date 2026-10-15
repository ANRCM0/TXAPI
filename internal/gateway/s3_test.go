package gateway

import (
	"context"
	"io"
	"math/rand"
	"minigateway/internal/access"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestS3ImmediateCooldownSurvivesStaleReload(t *testing.T) {
	channels := lifecycleChannels("http://upstream.test")
	for i := range channels[0].Credentials {
		channels[0].Credentials[i].ChannelID = 1
	}
	g, err := New(Options{Channels: channels})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	original := channels[0].Credentials[0]
	g.reportCredentialHealth("test", &original, 502, "upstream failed")
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		_, c := g.selectTarget(g.pickChannels("m", nil), nil, "", time.Now())
		if c == nil || c.ID == original.ID {
			t.Fatalf("observed bad credential selected before persistence: %+v", c)
		}
	}
	// Explicit manual recovery carries a new generation and supersedes local state.
	channels[0].Credentials[0].HealthGeneration++
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	_, c := g.selectTarget(g.pickChannels("m", nil), map[uint]bool{2: true, 3: true}, "", time.Now())
	if c == nil || c.ID != 1 {
		t.Fatal("manual recovery did not override runtime cooldown")
	}
}
func TestS3WeightedChannelsAndPriorityFallback(t *testing.T) {
	c := func(id int, p, w int) *channel {
		return &channel{id: id, priority: p, weight: w, credentials: []dbmodel.Credential{{ID: uint(id), APIKey: "key", Status: dbmodel.CredentialActive}}}
	}
	first, second, backup := c(1, 5, 1), c(2, 5, 3), c(3, 1, 100)
	g := testCoordinator(t)
	for range 100 {
		ch, _ := g.selectTarget([]*channel{first, second, backup}, nil, "", time.Now())
		if ch == backup {
			t.Fatal("backup selected before higher priority exhausted")
		}
	}
	ch, _ := g.selectTarget([]*channel{first, second, backup}, map[uint]bool{1: true, 2: true}, "", time.Now())
	if ch != backup {
		t.Fatal("backup not selected after exhaustion")
	}
	rng := rand.New(rand.NewSource(31))
	hits := 0
	for range 20000 {
		if chooseWeightedChannel([]*channel{first, second}, rng.Intn) == first {
			hits++
		}
	}
	if hits < 4500 || hits > 5500 {
		t.Fatalf("weighted distribution outside tolerance: %d/20000", hits)
	}
	if chooseWeightedChannel([]*channel{{weight: 0}, {weight: -1}}, func(n int) int {
		if n != 2 {
			t.Fatal(n)
		}
		return 1
	}) == nil {
		t.Fatal("invalid weights panicked")
	}
}
func TestS3RetryUpstreamCredentialAuthWithoutChangingModel(t *testing.T) {
	for _, code := range []int{401, 403, 429, 502} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			hits := 0
			seen := map[string]bool{}
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hits++
				key := r.Header.Get("Authorization")
				if seen[key] {
					t.Fatal("retried same credential")
				}
				seen[key] = true
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"model":"m"`) {
					t.Fatalf("retry changed authorized model: %s", body)
				}
				status, reply := 200, `{"choices":[]}`
				if hits == 1 {
					status, reply = code, `{"error":{"message":"bad credential"}}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})}
			g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client, RetryTimes: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.Allowlist, Models: []string{"m"}}})
			c, w := relayContext(ctx)
			g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), []string{"m"}, meta.ModeChatCompletions, time.Second)
			if hits != 2 || w.Code != 200 {
				t.Fatalf("credential auth failover: hits=%d status=%d", hits, w.Code)
			}
		})
	}
}
func TestS3PickerOnlySelectsIdentityAndCannotMutateSnapshots(t *testing.T) {
	ch := &channel{id: 1, credentials: []dbmodel.Credential{{ID: 1, APIKey: "trusted", Status: dbmodel.CredentialActive}, {ID: 2, APIKey: "cool", Status: dbmodel.CredentialCooldown, CooldownUntil: time.Now().Add(time.Hour).Unix()}}}
	g := testCoordinator(t)
	g.picker = func(creds []dbmodel.Credential, excluded map[uint]bool, _ string, _ time.Time) (*dbmodel.Credential, bool) {
		creds[0].APIKey = "mutated"
		delete(excluded, 1)
		return &dbmodel.Credential{ID: 1, APIKey: "injected"}, true
	}
	got := g.pickCredentialWithSession(ch, nil, "", time.Now())
	if got == nil || got.APIKey != "trusted" || ch.credentials[0].APIKey != "trusted" {
		t.Fatalf("picker changed key material: %+v", got)
	}
	if got = g.pickCredentialWithSession(ch, map[uint]bool{1: true}, "", time.Now()); got != nil {
		t.Fatalf("picker escaped exclusion: %+v", got)
	}
}

func TestS3AllCoolingChannelsBecomeEligibleWithoutReload(t *testing.T) {
	channels := lifecycleChannels("http://upstream.test")
	for i := range channels[0].Credentials {
		channels[0].Credentials[i].Status = dbmodel.CredentialCooldown
		channels[0].Credentials[i].CooldownUntil = time.Now().Add(time.Hour).Unix()
	}
	g, err := New(Options{Channels: channels})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, c := g.selectTarget(g.pickChannels("m", nil), nil, "", time.Now()); c != nil {
		t.Fatal("future cooldown was ignored")
	}
	if _, c := g.selectTarget(g.pickChannels("m", nil), nil, "", time.Now().Add(2*time.Hour)); c == nil {
		t.Fatal("channel discarded while cooling; expiry requires reload")
	}
}

func TestS3DelayedPersistenceCannotRewindNewerRuntimeFailures(t *testing.T) {
	channels := lifecycleChannels("http://upstream.test")
	g, err := New(Options{Channels: channels})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	original := channels[0].Credentials[0]
	g.reportCredentialHealth("test", &original, 502, "one")
	g.reportCredentialHealth("test", &original, 502, "two")
	persisted := channels[0].Credentials[0]
	persisted.FailCount = 1
	persisted.Status = dbmodel.CredentialCooldown
	persisted.CooldownUntil = time.Now().Add(30 * time.Second).Unix()
	persisted.UpdatedAt = time.Now().Add(time.Second)
	channels[0].Credentials[0] = persisted
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	g.reg.mu.RLock()
	got := g.reg.channels[1].credentials[0]
	g.reg.mu.RUnlock()
	if got.FailCount != 2 {
		t.Fatalf("async persistence rewound live health: %+v", got)
	}
}

func TestS3RetryBudgetAndRouteReloadKeepAuthorization(t *testing.T) {
	for _, scenario := range []string{"bounded", "route revoked"} {
		t.Run(scenario, func(t *testing.T) {
			hits := 0
			var g *Gateway
			channels := lifecycleChannels("http://upstream.test")
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits++
				if scenario == "route revoked" {
					changed := lifecycleChannels("http://upstream.test")
					changed[0].Models = dbmodel.StringList{"forbidden"}
					if err := g.Reload(changed); err != nil {
						t.Fatal(err)
					}
				}
				return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)), Header: make(http.Header)}, nil
			})}
			var err error
			g, err = New(Options{Channels: channels, Client: client, RetryTimes: 10})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.Allowlist, Models: []string{"m"}}})
			c, _ := relayContext(ctx)
			g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), []string{"m"}, meta.ModeChatCompletions, time.Second)
			want := 3
			if scenario == "route revoked" {
				want = 1
			}
			if hits != want {
				t.Fatalf("unbounded retry or unauthorized reloaded route: hits=%d want=%d", hits, want)
			}
		})
	}
}
