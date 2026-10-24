package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/foundation/contracts/observability/v1"
)

func TestRequestIDBoundsUntrustedCorrelation(t *testing.T) {
	for _, values := range [][]string{nil, {strings.Repeat("x", 65)}, {"one two"}, {"a,b"}, {"private\tdata"}, {"标识"}, {"first", "second"}} {
		e := gin.New()
		e.Use(RequestID())
		e.GET("/", func(c *gin.Context) {
			id := c.GetString(CtxRequestID)
			if id != observability.RequestID(c.Request.Context()) || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
				t.Errorf("unsafe request ID reached context: %q", id)
			}
			c.Status(http.StatusNoContent)
		})
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header["X-Request-Id"] = values
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(w.Header().Get("X-Request-Id")) {
			t.Errorf("unsafe ID echoed: %q", w.Header().Get("X-Request-Id"))
		}
	}
}

func TestRequestIDPreservesBoundedCorrelation(t *testing.T) {
	for _, id := range []string{"trace.A_1:part-2", strings.Repeat("a", 64)} {
		e := gin.New()
		e.Use(RequestID())
		e.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Request-Id", id)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Header().Get("X-Request-Id") != id {
			t.Fatal("valid correlation was replaced")
		}
	}
}
