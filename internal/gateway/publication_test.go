package gateway

import (
	"testing"

	"minigateway/internal/model"
)

func TestPublishRejectsWholeInvalidSnapshotAndRetainsAppliedPool(t *testing.T) {
	g := testCoordinator(t)
	good := makeChannel(1, "applied", []string{"m1"})
	if err := g.Publish([]model.Channel{good}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.Channel){
		func(ch *model.Channel) { ch.Type = "missing" },
		func(ch *model.Channel) { ch.BaseURL = "file:///tmp/upstream" },
		func(ch *model.Channel) { ch.BaseURL = "http://secret:password@example.com" },
	} {
		bad := makeChannel(2, "pending", []string{"m2"})
		change(&bad)
		if err := g.Publish([]model.Channel{bad}); err == nil {
			t.Fatal("invalid publication succeeded")
		}
		if len(g.pickChannels("m1", nil)) != 1 || len(g.pickChannels("m2", nil)) != 0 {
			t.Fatal("failed publication changed the applied routing index")
		}
		g.RefreshHealth([]model.Channel{bad})
		if len(g.pickChannels("m2", nil)) != 0 {
			t.Fatal("health refresh bypassed publication")
		}
	}
}

func TestRefreshHealthDoesNotPublishPendingConfigOrOverwriteNewerObservation(t *testing.T) {
	g := testCoordinator(t)
	applied := makeChannel(1, "applied", []string{"m1"})
	applied.Credentials[0].HealthObservedAt = 100
	if err := g.Publish([]model.Channel{applied}); err != nil {
		t.Fatal(err)
	}
	pending := makeChannel(1, "pending", []string{"m2"})
	pending.BaseURL = "http://pending.example.com"
	pending.Credentials[0].HealthObservedAt = 101
	pending.Credentials[0].Status = model.CredentialCooldown
	pending.Credentials[0].FailCount = 2
	pending.Credentials[0].CooldownUntil = 1 << 40
	g.RefreshHealth([]model.Channel{pending})
	g.reg.mu.RLock()
	ch := g.reg.channels[1]
	g.reg.mu.RUnlock()
	if ch.name != "applied" || ch.baseURL != applied.BaseURL || ch.models[0] != "m1" || ch.credentials[0].FailCount != 2 {
		t.Fatal("health-only refresh changed configuration or lost newer health")
	}
	stale := makeChannel(1, "stale", []string{"m3"})
	stale.Credentials[0].HealthObservedAt = 100
	g.RefreshHealth([]model.Channel{stale})
	g.reg.mu.RLock()
	got := g.reg.channels[1].credentials[0]
	g.reg.mu.RUnlock()
	if got.Status != model.CredentialCooldown || got.FailCount != 2 {
		t.Fatal("stale database health unlocked a cooling credential")
	}
	pending.Credentials[0].HealthGeneration++
	pending.Credentials[0].Status = model.CredentialActive
	g.RefreshHealth([]model.Channel{pending})
	g.reg.mu.RLock()
	got = g.reg.channels[1].credentials[0]
	g.reg.mu.RUnlock()
	if got.Status != model.CredentialCooldown {
		t.Fatal("pending manual changes crossed the generation boundary")
	}
}
