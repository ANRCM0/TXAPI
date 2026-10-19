package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/config"
	ledger "minigateway/internal/foundation/contracts/ownerledger/v1"
	"minigateway/internal/model"
	"minigateway/internal/ownerbilling"
	"minigateway/internal/store"
)

func TestOwnerBillingHTTPCurrentScopePrecisionAndPrivacy(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "owner-ledger-http.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var sessions []string
	var users []uint
	for i, role := range []string{"user", "user", "admin"} {
		name := fmt.Sprintf("owner-ledger-http-%d", i)
		user, err := st.CreateUser(&store.UserInput{Username: name, Password: "owner-ledger-test-password", Role: role, ModelMode: "all"})
		if err != nil {
			t.Fatal(err)
		}
		secret, _, err := st.Login(name, "owner-ledger-test-password")
		if err != nil {
			t.Fatal(err)
		}
		users, sessions = append(users, user.ID), append(sessions, secret)
	}
	key := "sk-owner-ledger-http-fixture-only"
	if _, err := st.CreateToken(&store.TokenInput{Name: "owner-ledger", Key: key, UserID: users[0], ModelMode: "all"}); err != nil {
		t.Fatal(err)
	}
	view, ok := st.LookupToken(key)
	if !ok {
		t.Fatal("fixture identity unavailable")
	}
	id := strings.Repeat("a", 32)
	now := time.Now()
	if err := st.DB().Create(&model.BillingBalance{OwnerType: "user", OwnerID: users[0], UsedUnits: 9007199254740993, ReservedUnits: 71}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingReservation{ID: id, UserID: users[0], TokenID: view.ID, OriginModel: "client-model", PriceVersion: "historical-price-version", State: "pending", ReservedUnits: 71,
		Reason: "private-provider-error", Fingerprint: "private-fingerprint", ProviderBudgetUnits: 99, ProviderHeldUnits: 88, CreatedAt: now, UpdatedAt: now, LeaseUntil: now.Add(-time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	h := fixtureHandler(st, nil, nil)
	h.ownerLedger = ownerbilling.New(st.OwnerLedger(), h.control, access.NewMemoryLimiter())
	router := gin.New()
	h.RegisterAccess(router.Group(""))
	request := func(method, path, session string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if session != "" {
			r.Header.Set("Authorization", "Bearer "+session)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d expected %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if method == "GET" && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("financial response may be cached", w.Code)
		}
		return w.Body.Bytes()
	}
	base := "/api/me/billing"
	request("GET", base+"/summary", "", 401)
	request("GET", base+"/summary", key, 401)
	request("GET", base+"/reservations/"+id, sessions[1], 404)
	request("GET", base+"/reservations/"+id, sessions[2], 404)
	request("GET", base+"/reservations/not-an-id", sessions[0], 404)
	request("GET", base+"/reservations/"+strings.Repeat("b", 32), sessions[0], 404)
	summary := request("GET", base+"/summary", sessions[0], 200)
	for _, exact := range []string{`"posted_used_units":"9007199254740993"`, `"held_units":"71"`, `"pending_count":"1"`, `"balance_known":true`} {
		if !strings.Contains(string(summary), exact) {
			t.Fatal("financial precision lost", exact, string(summary))
		}
	}
	unknown := request("GET", base+"/summary", sessions[1], 200)
	for _, exact := range []string{`"posted_used_units":null`, `"held_units":null`, `"pending_count":"0"`, `"balance_known":false`} {
		if !strings.Contains(string(unknown), exact) {
			t.Fatal("missing balance fabricated", exact, string(unknown))
		}
	}
	detail := request("GET", base+"/reservations/"+id, sessions[0], 200)
	for _, exact := range []string{`"charge_units":null`, `"held_units":"71"`, `"original_reserved_units":"71"`, `"price_version":"historical-price-version"`} {
		if !strings.Contains(string(detail), exact) {
			t.Fatal("expired pending hold/unknown charge changed", exact, string(detail))
		}
	}
	for _, private := range []string{"private-provider-error", "private-fingerprint", "provider_", "credential", "attempts", "reason", "user_id", "evidence", "actor_id"} {
		if strings.Contains(string(detail), private) {
			t.Fatal("private financial columns exposed", private, string(detail))
		}
	}
	var page ledger.Page
	if err := json.Unmarshal(request("GET", base+"/reservations?state=pending&limit=1&offset=0", sessions[0], 200), &page); err != nil || len(page.Data) != 1 || page.HasMore {
		t.Fatal(page, err)
	}
	if err := json.Unmarshal(request("GET", base+"/reservations?token_id=99999999", sessions[0], 200), &page); err != nil || len(page.Data) != 0 {
		t.Fatal("foreign token query leaked data", page, err)
	}
	for _, query := range []string{"state=", "state=unknown", "limit=0", "limit=101", "limit=01", "limit=1&limit=2", "offset=-1", "offset=10001", "offset=00", "token_id=0", "token_id=01", "token_id=18446744073709551615", "user_id=1", "owner_id=1", "surprise=1", "limit=%ZZ"} {
		request("GET", base+"/reservations?"+query, sessions[0], 400)
	}
	request("GET", base+"/summary?limit=1", sessions[0], 400)
	request("GET", base+"/reservations/"+id+"?state=pending", sessions[0], 400)
	request("POST", base+"/reservations/"+id+"/resolve", sessions[0], 404)
	if err := st.DB().Model(&model.Token{}).Where("id = ?", view.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	request("GET", base+"/reservations/"+id, sessions[0], 200)
	if err := st.DB().Delete(&model.Token{}, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	request("GET", base+"/reservations/"+id, sessions[0], 200)
	h.ownerLedger = nil
	request("GET", base+"/summary", sessions[0], 503)
	h.ownerLedger = ownerbilling.New(st.OwnerLedger(), h.control, access.NewMemoryLimiter())
	if err := st.Logout(sessions[0]); err != nil {
		t.Fatal(err)
	}
	request("GET", base+"/summary", sessions[0], 401)
}

type failedOwnerLedger struct{ err error }

func (f failedOwnerLedger) Summary(context.Context, ledger.Access) (ledger.Summary, error) {
	return ledger.Summary{}, f.err
}
func (f failedOwnerLedger) List(context.Context, ledger.Access, ledger.Query) (ledger.Page, error) {
	return ledger.Page{}, f.err
}
func (f failedOwnerLedger) Read(context.Context, ledger.Access, string) (ledger.Detail, error) {
	return ledger.Detail{}, f.err
}

func TestOwnerBillingHTTPFixedErrorsNeverLeakRepositoryDetails(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{{ledger.ErrInvalid, 400}, {ledger.ErrUnauthorized, 401}, {ledger.ErrNotFound, 404}, {ledger.ErrRateLimited, 429}, {errors.New("private SQL database/path provider-secret"), 503}} {
		h := &Handler{ownerLedger: failedOwnerLedger{test.err}}
		router := gin.New()
		router.GET("/summary", h.ownerBillingSummary)
		router.GET("/reservations", h.ownerBillingList)
		router.GET("/reservations/:id", h.ownerBillingRead)
		for _, path := range []string{"/summary", "/reservations", "/reservations/record"} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "retry with the same decision") {
				t.Fatal(path, w.Code, w.Body.String())
			}
			if test.status == 429 && w.Header().Get("Retry-After") != "60" {
				t.Fatal("query limit missing Retry-After")
			}
		}
	}
}
