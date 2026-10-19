package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	routes "minigateway/internal/foundation/contracts/routes/v1"
	httpoutput "minigateway/internal/foundation/httpoutput/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/routeconsole"
	"minigateway/internal/store"
)

func TestRoutingHTTPScopesAndCurrentSessionAuthority(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "routing.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var sessions []string
	var users []model.User
	for i := 0; i < 3; i++ {
		role := model.RoleUser
		if i == 2 {
			role = model.RoleAdmin
		}
		name := fmt.Sprintf("routing-user-%d", i)
		user, err := st.CreateUser(&store.UserInput{Username: name, Password: "routing-password-only", Role: role, ModelMode: "all"})
		if err != nil {
			t.Fatal(err)
		}
		session, _, err := st.Login(name, "routing-password-only")
		if err != nil {
			t.Fatal(err)
		}
		sessions, users = append(sessions, session), append(users, *user)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(),
		RouteDiagnostics: true, BillingMode: billing.ModeDisabled, Channels: []model.Channel{{
			ID: 19, Name: "private-route-channel", Type: "openai", BaseURL: upstream.URL, Status: model.StatusEnabled,
			Models: model.StringList{"m"}, Weight: 1, ModelMapping: model.StringMap{"m": "private-actual-model"},
			Credentials: []model.Credential{{ID: 29, APIKey: "private-route-upstream-key", Status: model.CredentialActive, Weight: 1}},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	var keys []*model.Token
	for i := 0; i < 2; i++ {
		key, err := st.IssueToken(users[i].ID, &store.TokenInput{Name: "routing-fixture", ModelMode: "all"})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: users[i].ID, TokenID: key.ID, Policy: access.Policy{Mode: access.All}})
		ctx = access.WithRequestID(ctx, "equal-across-owners")
		result := gateway.NewHTTPExecutor(gw).Execute(ctx, httpoutput.New(httptest.NewRecorder()), execution.Invocation{Operation: execution.Chat, Body: []byte(`{"model":"m","messages":[{"role":"user","content":"private-routing-prompt"}]}`)})
		if result.StatusCode != 200 {
			t.Fatal(result)
		}
	}
	h := fixtureHandler(st, nil, nil)
	h.routes = routeconsole.New(gw.Diagnostics(), h.control, access.NewMemoryLimiter())
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
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("routing metadata response can be cached")
		}
		for _, secret := range []string{"private-route-upstream-key", "private-route-channel", "private-routing-prompt", upstream.URL} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private source leaked", secret)
			}
		}
		return w.Body.Bytes()
	}
	request("/api/me/routing", "", 401)
	request("/api/me/routing", "sk-model-call-key", 401)
	request("/api/admin/routing", sessions[0], 403)
	var own, admin routes.Page
	body := request("/api/me/routing?request_id=equal-across-owners", sessions[0], 200)
	if err = json.Unmarshal(body, &own); err != nil || len(own.Data) != 1 || own.Data[0].UserID != users[0].ID || !own.Enabled {
		t.Fatal(string(body), err)
	}
	for _, hidden := range []string{`"channel_id"`, `"credential_id"`, "private-actual-model"} {
		if strings.Contains(string(body), hidden) {
			t.Fatal("owner acquired target metadata", hidden)
		}
	}
	body = request("/api/admin/routing?request_id=equal-across-owners", sessions[2], 200)
	if err = json.Unmarshal(body, &admin); err != nil || len(admin.Data) != 2 {
		t.Fatal(string(body), err)
	}
	foreign := admin.Data[0]
	for _, trace := range admin.Data {
		if trace.UserID != users[0].ID {
			foreign = trace
		}
		if trace.Attempts[0].ActualModel != "private-actual-model" || trace.Attempts[0].CredentialID != 29 {
			t.Fatal("administrator diagnostic facts missing", trace)
		}
	}
	request("/api/me/routing/"+foreign.ID, sessions[0], 404)
	request("/api/admin/routing/"+foreign.ID, sessions[2], 200)
	request("/api/me/routing/"+own.Data[0].ID, sessions[0], 200)
	if err = st.DeleteOwnedToken(users[0].ID, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	request("/api/me/routing/"+own.Data[0].ID, sessions[0], 200)
	request("/api/me/routing/"+own.Data[0].ID+"?extra=1", sessions[0], 400)
	for _, query := range []string{"user_id=2", "limit=0", "limit=101", "offset=-1", "offset=10001", "limit=1&limit=2",
		"token_id=0", "token_id=bad", "request_id=", "request_id=%0A", "request_id=" + strings.Repeat("a", 65), "unknown=1"} {
		request("/api/me/routing?"+query, sessions[0], 400)
	}
	if err = st.DB().Model(&model.User{}).Where("id = ?", users[2].ID).Update("role", model.RoleUser).Error; err != nil {
		t.Fatal(err)
	}
	request("/api/admin/routing/"+foreign.ID, sessions[2], 403)
	if _, err = h.routes.Read(context.Background(), routes.Access{Session: sessions[2], Administrator: true}, foreign.ID); !errors.Is(err, routes.ErrForbidden) {
		t.Fatal("direct service reused demoted administrator authority", err)
	}
	if err = st.Logout(sessions[1]); err != nil {
		t.Fatal(err)
	}
	if _, err = h.routes.Read(context.Background(), routes.Access{Session: sessions[1]}, foreign.ID); !errors.Is(err, routes.ErrUnauthorized) {
		t.Fatal("logged-out direct service retained access", err)
	}
	if err = st.DB().Model(&model.User{}).Where("id = ?", users[0].ID).Update("status", model.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	request("/api/me/routing", sessions[0], 401)
	if _, err = h.routes.List(context.Background(), routes.Access{Session: sessions[0]}, routes.Query{}); !errors.Is(err, routes.ErrUnauthorized) {
		t.Fatal("disabled direct service retained access", err)
	}
	if err = st.DB().Model(&model.User{}).Where("id = ?", users[0].ID).Update("status", model.UserStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	if err = st.DB().Delete(&users[0]).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = h.routes.Read(context.Background(), routes.Access{Session: sessions[0]}, own.Data[0].ID); !errors.Is(err, routes.ErrUnauthorized) {
		t.Fatal("soft-deleted direct service retained access", err)
	}
}
