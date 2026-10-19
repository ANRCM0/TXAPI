package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
	"minigateway/internal/taskconsole"
)

func TestTaskConsoleHTTPAuthorityAndCAS(t *testing.T) {
	st, e := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "task-http.db")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	sessions := []string{}
	owners := []uint{}
	for i := 0; i < 3; i++ {
		role := "user"
		if i == 2 {
			role = "admin"
		}
		name := fmt.Sprintf("console-owner-%d", i)
		u, e := st.CreateUser(&store.UserInput{Username: name, Password: "console-password-123", Role: role, ModelMode: "all"})
		if e != nil {
			t.Fatal(e)
		}
		secret, _, e := st.Login(name, "console-password-123")
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, secret)
		owners = append(owners, u.ID)
	}
	if e = st.SetSetting("background.digest_version", strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	id := "resp_" + strings.Repeat("a", 64)
	row := model.BackgroundJob{ID: strings.Repeat("b", 64), PublicID: id, UserID: owners[0], TokenID: 77, Model: "m", Stage: "remote_active", Revision: 1, CreatedAt: now.UnixMilli(), Deadline: now.Add(time.Hour).UnixMilli(), ContentExpiresAt: now.Add(time.Hour).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), Ciphertext: []byte("private-http-input"), Snapshot: []byte("private-http-upstream"), LastSequence: -1}
	if e = st.DB().Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	h := fixtureHandler(st, nil, nil)
	h.tasks = taskconsole.New(st.TaskControl(), h.control, access.NewMemoryLimiter())
	engine := gin.New()
	h.RegisterAccess(engine.Group(""))
	request := func(method, path, session, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if session != "" {
			r.Header.Set("Authorization", "Bearer "+session)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-http") {
			t.Fatal("content leaked")
		}
		return w
	}
	request("GET", "/api/me/background", "", "", 401)
	request("GET", "/api/me/background", "sk-wrong-credential", "", 401)
	request("GET", "/api/admin/background", sessions[0], "", 403)
	request("GET", "/api/me/background/"+id, sessions[1], "", 404)
	request("POST", "/api/me/background/"+id+"/cancel", sessions[1], "{\"revision\":\"1\"}", 404)
	for _, q := range []string{"user_id=1", "offset=-1", "offset=10001", "limit=101", "limit=0", "limit=1&limit=2", "stage=unknown", "token_id=no", "token_id=0", "extra=1", "stage=queued;bad"} {
		request("GET", "/api/me/background?"+q, sessions[0], "", 400)
	}
	request("GET", "/api/me/background", sessions[0], "", 200)
	request("GET", "/api/admin/background?user_id="+fmt.Sprint(owners[0]), sessions[2], "", 200)
	request("GET", "/api/admin/background/"+id, sessions[2], "", 200)
	for _, body := range []string{"{}", "{\"revision\":1}", "{\"revision\":\"01\"}", "{\"revision\":\"1\",\"revision\":\"2\"}", "{\"revision\":\"1\",\"extra\":true}", "{\"revision\":\"0\"}", strings.Repeat("x", 1025)} {
		request("POST", "/api/me/background/"+id+"/cancel", sessions[0], body, 400)
	}
	w := request("POST", "/api/me/background/"+id+"/cancel", sessions[0], "{\"revision\":\"1\"}", 200)
	var out struct {
		Data struct {
			Revision        string
			CancelRequested bool `json:"cancel_requested"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Data.Revision != "2" || !out.Data.CancelRequested {
		t.Fatal(w.Body.String())
	}
	request("POST", "/api/me/background/"+id+"/cancel", sessions[0], "{\"revision\":\"1\"}", 409)
	request("POST", "/api/me/background/"+id+"/cancel", sessions[0], "{\"revision\":\"2\"}", 200)
	if e = st.Logout(sessions[0]); e != nil {
		t.Fatal(e)
	}
	request("GET", "/api/me/background", sessions[0], "", 401)
}
