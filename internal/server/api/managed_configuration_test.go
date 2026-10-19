package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/store"
)

type managedServiceStub struct {
	managed.Service
	calls    int
	revision int64
	change   cfg.Change
	err      error
}

func (s *managedServiceStub) CreateManagedChannel(_ context.Context, in *managed.ManagedChannelInput) (*managed.ManagedChannel, cfg.Change, error) {
	s.calls++
	if in.Channel.APIKey != "" {
		panic("static key crossed managed input")
	}
	return &managed.ManagedChannel{Credential: managed.ManagedCredential{CredentialID: 9, Revision: 1}}, s.change, s.err
}
func (s *managedServiceStub) GetManagedCredential(context.Context, uint) (*managed.ManagedCredential, error) {
	s.calls++
	return &managed.ManagedCredential{CredentialID: 9, Revision: 1}, s.err
}
func (s *managedServiceStub) UpdateManagedCredential(_ context.Context, _ uint, revision int64, _ *managed.ManagedBindingInput) (*managed.ManagedCredential, cfg.Change, error) {
	s.calls++
	s.revision = revision
	return &managed.ManagedCredential{CredentialID: 9, Revision: revision + 1}, s.change, s.err
}
func (s *managedServiceStub) DisableManagedCredential(_ context.Context, _ uint, revision int64) (*managed.ManagedCredential, cfg.Change, error) {
	s.calls++
	s.revision = revision
	return &managed.ManagedCredential{CredentialID: 9, Revision: revision + 1}, s.change, s.err
}

func TestManagedAdminHTTPRejectsWrongAuthorityAndInvalidBodies(t *testing.T) {
	_, st := newAPITestEngine(t)
	if _, err := st.BootstrapAdmin("managed-admin", "managed-admin-password-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(&store.UserInput{Username: "managed-user", Password: "managed-user-password-fixture", ModelMode: "all"}); err != nil {
		t.Fatal(err)
	}
	admin, _, err := st.Login("managed-admin", "managed-admin-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := st.Login("managed-user", "managed-user-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	stub := &managedServiceStub{}
	h := fixtureHandler(st, nil, nil)
	h.managed = stub
	e := gin.New()
	h.RegisterAccess(e.Group(""))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("X-User-ID", "1")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	for _, route := range []struct{ method, path string }{{"POST", "/api/admin/managed-channels"}, {"GET", "/api/admin/managed-credentials/9"}, {"PUT", "/api/admin/managed-credentials/9"}, {"POST", "/api/admin/managed-credentials/9/disable"}} {
		for _, auth := range []struct {
			token string
			code  int
		}{{"", 401}, {user, 403}} {
			w := request(route.method, route.path, `{}`, auth.token)
			if w.Code != auth.code {
				t.Fatalf("wrong authority %s: %d %s", route.path, w.Code, w.Body)
			}
		}
	}
	if stub.calls != 0 {
		t.Fatal("unauthorized mutation reached configuration")
	}
	valid := `{"channel":{"name":"managed","type":"openai_siwc","base_url":"https://api.openai.com/v1","models":["m"]},"binding":{"account_id":"acct","owner_id":1,"token_id":2,"model":"m"}}`
	for _, body := range []string{strings.Replace(valid, `"name":"managed"`, `"api_key":"", "name":"managed"`, 1), valid + `{}`, strings.Replace(valid, `"model":"m"`, `"unknown":1,"model":"m"`, 1)} {
		if w := request("POST", "/api/admin/managed-channels", body, admin); w.Code != 400 {
			t.Fatalf("invalid input accepted: %d %s", w.Code, w.Body)
		}
	}
	for _, raw := range []string{`1`, `"0"`, `"01"`, `"-1"`, `"+1"`, `"9223372036854775808"`, `null`} {
		if w := request("POST", "/api/admin/managed-credentials/9/disable", `{"expected_revision":`+raw+`}`, admin); w.Code != 400 {
			t.Fatalf("invalid revision accepted %s: %d", raw, w.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid body reached mutation")
	}
	w := request("POST", "/api/admin/managed-channels", valid, admin)
	if w.Code != 201 || strings.Contains(w.Body.String(), "api_key") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = request("POST", "/api/admin/managed-credentials/9/disable", `{"expected_revision":"9007199254740993"}`, admin)
	if w.Code != 200 || stub.revision != 9007199254740993 || !strings.Contains(w.Body.String(), `"revision":"9007199254740994"`) {
		t.Fatalf("revision precision lost: %d %s", w.Code, w.Body)
	}
	for _, item := range []struct {
		err  error
		code int
	}{{managed.ErrConflict, 409}, {managed.ErrNotFound, 404}, {managed.ErrUnavailable, 503}, {auth.ErrInvalid, 400}, {auth.ErrEncryptionRequired, 503}, {errors.New("private-access-token"), 503}} {
		stub.err = item.err
		if w := request("GET", "/api/admin/managed-credentials/9", "", admin); w.Code != item.code || strings.Contains(w.Body.String(), "private-access-token") {
			t.Fatalf("status for %v: %d %s", item.err, w.Code, w.Body)
		}
	}
	stub.err = &cfg.SavedError{Cause: errors.New("private-access-token")}
	stub.change = cfg.Change{Saved: true, Publication: cfg.PublicationStatus{DesiredRevision: 2, AppliedRevision: 1}}
	w = request("POST", "/api/admin/managed-channels", valid, admin)
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"saved":true`) || strings.Contains(w.Body.String(), "private-access-token") || w.Header().Get("X-TXAPI-Publication-State") != "pending" {
		t.Fatalf("committed failure: %d %s", w.Code, w.Body)
	}
}
