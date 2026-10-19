package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/config"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
	"minigateway/internal/usageconsole"
)

func TestUsageHistoryHTTPCurrentPermissionsExactStringsAndPrivateProjection(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "history-http.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var sessions []string
	var users []uint
	for i, role := range []string{"user", "user", "admin"} {
		name := fmt.Sprintf("usage-http-%d", i)
		user, err := st.CreateUser(&store.UserInput{Username: name, Password: "usage-http-test-password", Role: role, ModelMode: "all"})
		if err != nil {
			t.Fatal(err)
		}
		secret, _, err := st.Login(name, "usage-http-test-password")
		if err != nil {
			t.Fatal(err)
		}
		users, sessions = append(users, user.ID), append(sessions, secret)
	}
	key := "sk-usage-http-fixture-only"
	if _, err := st.CreateToken(&store.TokenInput{Name: "usage-http", Key: key, UserID: users[0], ModelMode: "all"}); err != nil {
		t.Fatal(err)
	}
	view, ok := st.LookupToken(key)
	if !ok {
		t.Fatal("fixture identity unavailable")
	}
	ctx := access.WithPrincipal(context.Background(), view.Principal())
	recorder := st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 16, OwnerRecords: 8, RetentionSeconds: 60})
	id := "usage_" + strings.Repeat("a", 32)
	start := history.Start{ID: id, RequestID: "correlation-only", Model: "client-model", Operation: v1.Responses, MaxAttempts: 1, QuotationVersion: strings.Repeat("b", 64)}
	if err := recorder.Begin(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Prepare(ctx, history.Target{ID: id, Number: 1, ChannelID: 71, CredentialID: 82, ActualModel: "private-actual", Group: "private-group", Protocol: "private-protocol"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MayDispatch(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	large, zero := int64(9007199254740993), int64(0)
	host := history.HostFacts{Phase: v1.ResponseReceived, Status: 200, UpstreamStatus: 200, HTTPStatus: 200, ProtocolComplete: true, DeliveryCommitted: true, BytesWritten: 123, Termination: v1.Success}
	observation := v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: &large, OutputTokens: &zero, TotalTokens: &large}, Breakdown: v3.TokenBreakdown{CachedInputTokens: &zero, CacheWriteInputTokens: &zero}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "responses_native_tokens_v2"}
	if err := recorder.Observe(ctx, history.Receipt{ID: id, Number: 1, Host: host, Usage: observation}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish(ctx, history.Completion{ID: id, Host: host}); err != nil {
		t.Fatal(err)
	}
	h := fixtureHandler(st, nil, nil)
	h.usageHistory = usageconsole.New(st.UsageHistory(true, 60), h.control, access.NewMemoryLimiter())
	router := gin.New()
	h.RegisterAccess(router.Group(""))
	request := func(path, session string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		if session != "" {
			r.Header.Set("Authorization", "Bearer "+session)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d expected %d: %s", path, w.Code, want, w.Body.String())
		}
		if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("usage metadata may be cached")
		}
		return w.Body.Bytes()
	}
	ownerPath, adminPath := "/api/me/usage-records", "/api/admin/usage-records"
	request(ownerPath, "", 401)
	request(ownerPath, key, 401)
	request(adminPath, sessions[0], 403)
	request(ownerPath+"/"+id, sessions[1], 404)
	request(ownerPath+"/usage_"+strings.Repeat("c", 32), sessions[0], 404)
	owner := request(ownerPath+"/"+id, sessions[0], 200)
	for _, hidden := range []string{"private-actual", "private-group", "private-protocol", "channel_id", "credential_id", "quotation_version"} {
		if strings.Contains(string(owner), hidden) {
			t.Fatal("owner projection exposed private routing metadata", hidden, string(owner))
		}
	}
	for _, precise := range []string{`"input_tokens":"9007199254740993"`, `"output_tokens":"0"`, `"reasoning_output_tokens":null`, `"bytes_written":"123"`} {
		if !strings.Contains(string(owner), precise) {
			t.Fatal("unknown/zero/integer precision was lost", precise, string(owner))
		}
	}
	admin := request(adminPath+"/"+id, sessions[2], 200)
	if !strings.Contains(string(admin), `"actual_model":"private-actual"`) || !strings.Contains(string(admin), `"quotation_version":"`+start.QuotationVersion+`"`) {
		t.Fatal("admin could not inspect actual target/version", string(admin))
	}
	var page history.Page
	if err := json.Unmarshal(request(ownerPath+"?state=sealed&limit=1&offset=0", sessions[0], 200), &page); err != nil || len(page.Data) != 1 || !page.Enabled {
		t.Fatal("owner list did not retain record", page, err)
	}
	if err := json.Unmarshal(request(ownerPath, sessions[1], 200), &page); err != nil || len(page.Data) != 0 {
		t.Fatal("cross-owner list leaked record", page, err)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "offset=-1", "offset=10001", "state=unknown", "state=", "user_id=1", "token_id=0", "token_id=01", "token_id=18446744073709551615", "surprise=1"} {
		request(ownerPath+"?"+query, sessions[0], 400)
	}
	request(ownerPath+"/"+id+"?limit=1", sessions[0], 400)
	h.usageHistory = usageconsole.New(st.UsageHistory(false, 60), h.control, access.NewMemoryLimiter())
	if err := json.Unmarshal(request(ownerPath, sessions[0], 200), &page); err != nil || page.Enabled || len(page.Data) != 1 {
		t.Fatal("disabling capture erased retained records", page, err)
	}
	if err := st.Logout(sessions[0]); err != nil {
		t.Fatal(err)
	}
	request(ownerPath+"/"+id, sessions[0], 401)
}
