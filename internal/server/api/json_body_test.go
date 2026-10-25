package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/server/middleware"
)

func TestTrailingJSONCannotMutateConfiguration(t *testing.T) {
	for _, suffix := range []string{`{}`, `null`, `trailing`} {
		e, st := newAPITestEngine(t)
		before := len(st.ListChannels())
		body := `{"name":"must-not-save","type":"openai","base_url":"http://127.0.0.1:9912","models":["m1"],"api_key":"synthetic-key"}`
		code, _ := doAPI(t, e, http.MethodPost, "/api/channels", body+suffix, true)
		if code != http.StatusBadRequest || len(st.ListChannels()) != before {
			t.Fatalf("trailing %q: status=%d channels=%d want %d", suffix, code, len(st.ListChannels()), before)
		}
	}
}

func TestJSONBodyConsumesEOFAndPreservesValidation(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"name":"safe"}` + " \n\t", 204},
		{`{"name":"safe"}` + strings.Repeat(" ", 64), 413},
		{`{"name":"safe"}{}`, 400},
		{`{}`, 400},
	} {
		e := gin.New()
		e.Use(middleware.RequestBodyLimit(32))
		e.POST("/", func(c *gin.Context) {
			var in struct {
				Name string `json:"name" binding:"required"`
			}
			if bindJSON(c, &in) {
				c.Status(http.StatusNoContent)
			}
		})
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		r.ContentLength = -1
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("body result=%d want=%d", w.Code, tc.status)
		}
	}
}
