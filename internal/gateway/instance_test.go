package gateway

import (
	"minigateway/internal/model"
	"minigateway/internal/store"
	"testing"
)

func testCoordinator(t *testing.T) *Gateway {
	t.Helper()
	g, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func fixtureOwner(t *testing.T, st *store.Store) uint {
	t.Helper()
	user := model.User{Username: "fixture-owner", PasswordHash: "unused-fixture-only", Role: model.RoleUser, Status: model.UserStatusActive, ModelMode: "all"}
	if err := st.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user.ID
}
