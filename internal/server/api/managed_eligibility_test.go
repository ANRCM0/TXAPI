package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	eligibility "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/store"
)

type eligibilityStub struct {
	calls int
	in    eligibility.ResumeRequest
	err   error
}

func (s *eligibilityStub) ReadEligibility(context.Context, uint) (eligibility.Eligibility, error) {
	s.calls++
	return eligibility.Eligibility{CredentialID: 9, Revision: 9007199254740993}, s.err
}
func (s *eligibilityStub) ResumeEligibility(_ context.Context, in eligibility.ResumeRequest) (eligibility.Eligibility, error) {
	s.calls++
	s.in = in
	return eligibility.Eligibility{CredentialID: in.CredentialID, Revision: in.ExpectedGateRevision + 1}, s.err
}

func TestManagedEligibilityAdminAuthorityCASPrecisionAndRedaction(t *testing.T) {
	_, st := newAPITestEngine(t)
	if _, err := st.BootstrapAdmin("gate-admin", "gate-admin-password-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(&store.UserInput{Username: "gate-user", Password: "gate-user-password-fixture", ModelMode: "all"}); err != nil {
		t.Fatal(err)
	}
	admin, _, err := st.Login("gate-admin", "gate-admin-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := st.Login("gate-user", "gate-user-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	stub := &eligibilityStub{}
	h := fixtureHandler(st, nil, nil)
	h.eligibility = stub
	e := gin.New()
	h.RegisterAccess(e.Group(""))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Admin-Key", "synthetic-legacy-admin")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	path := "/api/admin/managed-credentials/9/eligibility"
	valid := `{"expected_binding_revision":"9007199254740993","expected_gate_revision":"9007199254740993","expected_gate_epoch":"9007199254740993"}`
	for _, method := range []string{"GET", "POST"} {
		p := path
		if method == "POST" {
			p += "/resume"
		}
		for _, item := range []struct {
			token string
			code  int
		}{{"", 401}, {user, 403}, {"synthetic-calling-key", 401}} {
			if w := request(method, p, valid, item.token); w.Code != item.code {
				t.Fatalf("authority %s: %d %s", method, w.Code, w.Body)
			}
		}
	}
	if stub.calls != 0 {
		t.Fatal("wrong authority reached repository")
	}
	for _, raw := range []string{`1`, `"0"`, `"01"`, `"-1"`, `"+1"`, `"9223372036854775808"`, `null`} {
		for _, field := range []string{"expected_binding_revision", "expected_gate_revision", "expected_gate_epoch"} {
			body := strings.Replace(valid, `"`+field+`":"9007199254740993"`, `"`+field+`":`+raw, 1)
			if w := request("POST", path+"/resume", body, admin); w.Code != 400 {
				t.Fatalf("invalid revision %s %s: %d", field, raw, w.Code)
			}
		}
	}
	for _, body := range []string{`{}`, valid + `{}`, strings.Replace(valid, `{`, `{"credential_id":9,`, 1)} {
		if w := request("POST", path+"/resume", body, admin); w.Code != 400 {
			t.Fatalf("invalid body: %d %s", w.Code, w.Body)
		}
	}
	if stub.calls != 0 {
		t.Fatal("invalid body reached repository")
	}
	w := request("POST", path+"/resume", valid, admin)
	if w.Code != 200 || stub.in.CredentialID != 9 || stub.in.ExpectedBindingRevision != 9007199254740993 || stub.in.ExpectedGateRevision != 9007199254740993 || stub.in.ExpectedGateEpoch != 9007199254740993 || !strings.Contains(w.Body.String(), `"revision":"9007199254740994"`) {
		t.Fatalf("precision lost: %d %s %+v", w.Code, w.Body, stub.in)
	}
	for _, item := range []struct {
		err  error
		code int
	}{{auth.ErrInvalid, 400}, {auth.ErrNotFound, 404}, {auth.ErrConflict, 409}, {eligibility.ErrCapacity, 503}, {errors.New("private-provider-body-token"), 503}} {
		stub.err = item.err
		if w := request("GET", path, "", admin); w.Code != item.code || strings.Contains(w.Body.String(), "private-provider") {
			t.Fatalf("error mapping: %d %s", w.Code, w.Body)
		}
	}
	h.eligibility = nil
	if w := request("GET", path, "", admin); w.Code != 503 {
		t.Fatal("disabled eligibility available")
	}
}
