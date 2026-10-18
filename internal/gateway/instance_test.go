package gateway

import (
	"time"

	dbmodel "minigateway/internal/model"
	"minigateway/internal/store"
	"testing"
)

func testCoordinator(t *testing.T) *Gateway {
	t.Helper()
	g, err := newRegisteredGateway(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func fixtureOwner(t *testing.T, st *store.Store) uint {
	t.Helper()
	user := dbmodel.User{Username: "fixture-owner", PasswordHash: "unused-fixture-only", Role: dbmodel.RoleUser, Status: dbmodel.UserStatusActive, ModelMode: "all"}
	if err := st.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func TestCustomPickerCannotEscapeCredentialPoolOrRetryExclusion(t *testing.T) {
	g := testCoordinator(t)
	ch := &channel{id: 3, credentials: []dbmodel.Credential{{ID: 1, ChannelID: 3, APIKey: "a", Status: dbmodel.CredentialActive}}}
	g.picker = func([]dbmodel.Credential, map[uint]bool, string, time.Time) (*dbmodel.Credential, bool) {
		return &dbmodel.Credential{ID: 99, ChannelID: 4, APIKey: "leak", Status: dbmodel.CredentialActive}, true
	}
	if got := g.pickCredentialWithSession(ch, nil, "s", time.Now()); got == nil || got.ID != 1 {
		t.Fatalf("invalid picker output should fall back to pool, got=%+v", got)
	}
	g.picker = func([]dbmodel.Credential, map[uint]bool, string, time.Time) (*dbmodel.Credential, bool) {
		return &dbmodel.Credential{ID: 1, ChannelID: 3, APIKey: "a", Status: dbmodel.CredentialActive}, true
	}
	if got := g.pickCredentialWithSession(ch, map[uint]bool{1: true}, "s", time.Now()); got != nil {
		t.Fatalf("picker bypassed retry exclusion: %+v", got)
	}
}
func TestChannelSelectionHonorsPriorityAndFixedWeights(t *testing.T) {
	low := &channel{id: 1, priority: 1, weight: 100}
	first := &channel{id: 2, priority: 2, weight: 1}
	second := &channel{id: 3, priority: 2, weight: 3}
	pool := []*channel{first, second}
	if got := chooseWeightedChannel(pool, func(int) int { return 0 }); got != first {
		t.Fatal("lower boundary selected wrong channel")
	}
	if got := chooseWeightedChannel(pool, func(int) int { return 1 }); got != second {
		t.Fatal("weight boundary selected wrong channel")
	}
	if got := chooseWeightedChannel([]*channel{low}, func(int) int { return 0 }); got != low {
		t.Fatal("single channel selection failed")
	}
	if got := chooseWeightedChannel(nil, func(int) int { return 0 }); got != nil {
		t.Fatal("empty pool selection should be nil")
	}
}
