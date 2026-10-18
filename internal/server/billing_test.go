package server

import (
	"context"
	"encoding/json"
	"minigateway/internal/protocol"
	"strings"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/server/middleware"
)

func TestS5AdminBillingWorkflowAndPermissionBoundary(t *testing.T) {
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	owner := f.createUser(t, admin, "alice", []string{"m1"})
	alice := f.login(t, "alice")
	keyID, callKey := f.issue(t, alice)
	if err := f.store.UpsertModelPrice(&model.ModelPrice{Model: "m1", InputPricePer1K: .15, OutputPricePer1K: .6}); err != nil {
		t.Fatal(err)
	}
	e := billing.New(f.store.DB(), billing.NewPriceTableWithHistory(f.store.PriceData, time.Hour, f.store.RecordPriceSnapshot))
	if err := e.EnableStrict("0.1", time.Minute); err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: f.store.Channels(), Billing: e, BillingMode: billing.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	setEngine := func(ledger *billing.Engine) {
		f.engine = fixtureEngine(fixtureDeps{Gateway: gw, Cfg: &config.Config{}, Store: f.store, Billing: ledger, AdminAuth: middleware.AdminAuth(testSecret)})
	}
	setEngine(e)
	ctx := context.Background()
	pre, err := e.Reserve(ctx, keyID, owner, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartAttempt(ctx, pre, model.BillingAttempt{Number: 1, ActualModel: "m1", Group: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Finish(ctx, pre, nil, "m1", "default", false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().Model(&model.BillingReservation{}).Where("id=?", pre.BillingID()).
		Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/billing/reservations/" + pre.BillingID()
	body := `{"action":"settle_usage","reason":"verified after timeout","evidence":"provider receipt case-123","usage":{"prompt_tokens":11,"completion_tokens":7}}`
	for _, token := range []string{"", alice, callKey} {
		want := 401
		if token == alice {
			want = 403
		}
		for _, tc := range []struct{ method, path, body string }{
			{"GET", "/api/admin/billing/reservations", ""},
			{"GET", path, ""},
			{"POST", path + "/resolve", body},
		} {
			if code, _, raw := f.request(t, tc.method, tc.path, tc.body, token); code != want {
				t.Fatalf("ledger authorization: %d want %d %s", code, want, raw)
			}
		}
	}
	contract := loadContract(t)
	for _, resource := range []string{"/api/admin/billing/reservations", path} {
		code, _, raw := f.request(t, "GET", resource, "", admin)
		if code != 200 || strings.Contains(raw, "Fingerprint") || strings.Contains(raw, "upstream-secret") {
			t.Fatalf("unsafe ledger detail: %d %s", code, raw)
		}
		contractPath := resource
		if resource == path {
			contractPath = "/api/admin/billing/reservations/{id}"
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		assertContractShape(t, contract, contract.Paths[contractPath]["get"].Responses["200"].Content["application/json"].Schema, value)
	}
	for _, bad := range []string{
		`{"action":"waive","reason":"test","evidence":"receipt","actor_id":999}`,
		`{"action":"waive","reason":"test","evidence":"receipt","actual_model":"free"}`,
		body + `{}`,
		`{"action":"settle_usage","reason":"test","evidence":"receipt","usage":{"prompt_tokens":11}}`,
	} {
		if code, _, raw := f.request(t, "POST", path+"/resolve", bad, admin); code != 400 {
			t.Fatalf("invalid financial decision accepted: %d %s", code, raw)
		}
	}
	for _, query := range []string{"state=typo", "limit=101", "limit=-1", "offset=-1", "user_id=no"} {
		if code, _, raw := f.request(t, "GET", "/api/admin/billing/reservations?"+query, "", admin); code != 400 {
			t.Fatalf("invalid ledger query accepted: %s %d %s", query, code, raw)
		}
	}
	if code, _, _ := f.request(t, "GET", "/api/admin/billing/reservations/"+strings.Repeat("0", 32), "", admin); code != 404 {
		t.Fatalf("missing reservation: %d", code)
	}
	for range 2 {
		code, _, raw := f.request(t, "POST", path+"/resolve", body, admin)
		if code != 200 {
			t.Fatalf("resolve: %d %s", code, raw)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		assertContractShape(t, contract, contract.Paths["/api/admin/billing/reservations/{id}/resolve"]["post"].Responses["200"].Content["application/json"].Schema, value)
		var reply struct {
			Data billing.ResolutionView `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &reply); err != nil || reply.Data.ActorID != 1 || reply.Data.ChargeUnits != "5850000" {
			t.Fatalf("untrusted actor or incorrect charge: %+v %v", reply, err)
		}
	}
	if code, _, raw := f.request(t, "POST", path+"/resolve", `{"action":"waive","reason":"change decision","evidence":"different"}`, admin); code != 409 {
		t.Fatalf("decision overwritten: %d %s", code, raw)
	}
	// The ledger remains inspectable while the monetary component is disabled.
	setEngine(billing.New(f.store.DB(), e.Prices()))
	if code, _, raw := f.request(t, "GET", path, "", admin); code != 200 {
		t.Fatalf("read-only ledger unavailable: %d %s", code, raw)
	}
	if code, _, raw := f.request(t, "POST", path+"/resolve", body, admin); code != 503 {
		t.Fatalf("read-only engine accepted a decision: %d %s", code, raw)
	}
}
