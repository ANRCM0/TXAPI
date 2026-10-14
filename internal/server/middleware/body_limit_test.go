package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestBodyLimit(4))
	e.POST("/body", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})

	chunked := httptest.NewRequest(http.MethodPost, "/body", strings.NewReader("12345"))
	chunked.ContentLength = -1
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, chunked)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunked body status = %d, want 413", rec.Code)
	}

	declared := httptest.NewRequest(http.MethodPost, "/body", strings.NewReader("1"))
	declared.ContentLength = 5
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, declared)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized declared body status = %d, want 413", rec.Code)
	}
}
