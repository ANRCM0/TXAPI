package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/quoteconsole"
	"minigateway/internal/store"
)

func TestQuotationHTTPStringsPermissionsAndStrictInput(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "quotation.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var sessions []string
	for i, role := range []string{"user", "admin"} {
		name := fmt.Sprintf("quotation-http-%d", i)
		if _, err := st.CreateUser(&store.UserInput{Username: name, Password: "quotation-http-password", Role: role, ModelMode: "all"}); err != nil {
			t.Fatal(err)
		}
		session, _, err := st.Login(name, "quotation-http-password")
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	book := billing.NewQuotationBook(st.RecordPriceSnapshot, st.QuotationHistory)
	h := fixtureHandler(st, nil, nil)
	h.quotation = quoteconsole.New(book, h.control, access.NewMemoryLimiter())
	router := gin.New()
	h.RegisterAccess(router.Group(""))
	request := func(method, path, body, token string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d != %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("quotation may be cached")
		}
		return w.Body.Bytes()
	}
	currentPath, calculatePath := "/api/admin/quotations/current", "/api/admin/quotations/calculate"
	request("GET", currentPath, "", "", 401)
	request("GET", currentPath, "", "sk-model-call-key", 401)
	request("GET", currentPath, "", sessions[0], 403)
	if raw := request("GET", currentPath, "", sessions[1], 200); !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatal("default quotation is enabled", string(raw))
	}
	if err := book.Replace(billing.QuotationConfig{Currency: "ACCOUNT_UNIT", Prices: []billing.QuotationPrice{{Model: "m", UncachedInputUnitsPer1K: "1000", CachedInputUnitsPer1K: "100", CacheWriteInputUnitsPer1K: "2000", OutputUnitsPer1K: "3000"}}, GroupRateUnits: map[string]string{"default": "1000000000"}}); err != nil {
		t.Fatal(err)
	}
	var current struct{ Data struct{ Version string } }
	if err := json.Unmarshal(request("GET", currentPath, "", sessions[1], 200), &current); err != nil {
		t.Fatal(err)
	}
	valid := `{"price_version":"` + current.Data.Version + `","model":"m","group":"default","usage":{"input_tokens":"10","output_tokens":"6","total_tokens":"16","cached_input_tokens":"3","cache_write_input_tokens":"2","reasoning_output_tokens":"4","completeness":"complete","source":"provider_reported","profile":"responses_native_tokens_v2"}}`
	if raw := request("POST", calculatePath, valid, sessions[1], 200); !strings.Contains(string(raw), `"amount_units":"28"`) || !strings.Contains(string(raw), `"known":true`) {
		t.Fatal("selling partition/rounding was not exact", string(raw))
	}
	unknown := strings.Replace(valid, `"cached_input_tokens":"3"`, `"cached_input_tokens":null`, 1)
	if raw := request("POST", calculatePath, unknown, sessions[1], 200); !strings.Contains(string(raw), `"known":false`) || !strings.Contains(string(raw), `"amount_units":""`) || !strings.Contains(string(raw), "usage_unknown") {
		t.Fatal("missing amount became zero", string(raw))
	}
	for name, body := range map[string]string{
		"number":             strings.Replace(valid, `"input_tokens":"10"`, `"input_tokens":10`, 1),
		"leading zero":       strings.Replace(valid, `"input_tokens":"10"`, `"input_tokens":"010"`, 1),
		"negative":           strings.Replace(valid, `"input_tokens":"10"`, `"input_tokens":"-1"`, 1),
		"overflow":           strings.Replace(valid, `"input_tokens":"10"`, `"input_tokens":"9223372036854775808"`, 1),
		"fraction":           strings.Replace(valid, `"input_tokens":"10"`, `"input_tokens":"1.0"`, 1),
		"duplicate":          strings.Replace(valid, `"model":"m"`, `"model":"m","model":"m"`, 1),
		"case collision":     strings.Replace(valid, `"model":"m"`, `"model":"m","Model":"m"`, 1),
		"unknown root":       strings.Replace(valid, `"model":"m"`, `"extra":0,"model":"m"`, 1),
		"unknown quantity":   strings.Replace(valid, `"input_tokens":"10"`, `"surprise":0,"input_tokens":"10"`, 1),
		"case quantity":      strings.Replace(valid, `"input_tokens":"10"`, `"INPUT_TOKENS":"10"`, 1),
		"trailing":           valid + ` {}`,
		"too large":          strings.Replace(valid, `"model":"m"`, `"model":"`+strings.Repeat("x", 17*1024)+`"`, 1),
		"inconsistent total": strings.Replace(valid, `"total_tokens":"16"`, `"total_tokens":"15"`, 1),
	} {
		t.Run(name, func(t *testing.T) { request("POST", calculatePath, body, sessions[1], 400) })
	}
	request("GET", currentPath+"?unknown=1", "", sessions[1], 400)
	request("POST", calculatePath+"?model=m", valid, sessions[1], 400)
	request("POST", calculatePath, valid, sessions[0], 403)
	request("POST", calculatePath, strings.Replace(valid, current.Data.Version, strings.Repeat("0", 64), 1), sessions[1], 404)
	if err := st.Logout(sessions[1]); err != nil {
		t.Fatal(err)
	}
	request("GET", currentPath, "", sessions[1], 401)
	request("POST", calculatePath, valid, sessions[1], 401)
}
