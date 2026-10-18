package generationhttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

type serviceStub struct {
	calls int
	got   execution.Invocation
	ctx   context.Context
}

func (s *serviceStub) Execute(ctx context.Context, out execution.Output, in execution.Invocation) execution.Outcome {
	s.calls++
	s.got, s.ctx = in, ctx
	_ = out.Commit(ctx, execution.ResponseMetadata{StatusCode: 200, ContentType: "application/json", Values: map[string]string{"X-TXAPI-Billing-ID": "billing-fixture"}})
	_, _ = out.Write(ctx, []byte(`{"native":true}`))
	return execution.Outcome{}
}
func (*serviceStub) Models(context.Context) []execution.Model { return []execution.Model{} }

func TestHTTPEntryUsesPublicExecutionAndPreservesOperationAndMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, operation := range []execution.Operation{execution.Chat, execution.Embeddings} {
		t.Run(string(operation), func(t *testing.T) {
			service := &serviceStub{}
			handler := New(service)
			payload := []byte(`{"model":"m","unmodeled":{"keep":true}}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/fixture", bytes.NewReader(payload))
			c.Request.Header.Set("X-Session-Id", "thread-fixture")
			if operation == execution.Chat {
				handler.HandleChatCompletions(c)
			} else {
				handler.HandleEmbeddings(c)
			}
			if service.calls != 1 || service.got.Operation != operation || !bytes.Equal(service.got.Body, payload) || service.got.SessionID != "thread-fixture" || service.ctx != c.Request.Context() {
				t.Fatalf("HTTP request did not reach the public generation boundary intact: %+v", service.got)
			}
			if recorder.Code != 200 || recorder.Header().Get("X-TXAPI-Billing-ID") != "billing-fixture" || recorder.Body.String() != `{"native":true}` {
				t.Fatalf("output metadata/body changed: %+v", recorder)
			}
		})
	}
}

type failedBody struct{ err error }

func (b failedBody) Read([]byte) (int, error) { return 0, b.err }
func (failedBody) Close() error               { return nil }
func TestHTTPReadFailureDoesNotStartExecution(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		kind string
	}{
		{&http.MaxBytesError{Limit: 4}, 413, "request_too_large"},
		{errors.New("private body read failure"), 400, "invalid_request_error"},
	} {
		service := &serviceStub{}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/fixture", nil)
		c.Request.Body = failedBody{tc.err}
		New(service).HandleChatCompletions(c)
		if recorder.Code != tc.code || !strings.Contains(recorder.Body.String(), tc.kind) || strings.Contains(recorder.Body.String(), "private") || service.calls != 0 {
			t.Fatalf("read failure started execution or changed public error: %s", recorder.Body)
		}
	}
}
