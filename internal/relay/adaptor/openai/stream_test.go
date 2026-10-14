package openai

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
)

var errUpstreamRead = errors.New("upstream connection reset")

type errorAfterData struct {
	data []byte
	done bool
}

func (r *errorAfterData) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, errUpstreamRead
}

func callStreamResponse(body io.Reader) (*httptest.ResponseRecorder, *model.ErrorWithStatusCode) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}
	_, relayErr := (&Adaptor{}).DoResponse(ctx, resp, &meta.Meta{IsStream: true})
	return recorder, relayErr
}

func TestStreamReadErrorIsReturnedWithoutDone(t *testing.T) {
	upstream := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	recorder, err := callStreamResponse(&errorAfterData{data: []byte(upstream)})
	if err == nil || err.StatusCode != http.StatusBadGateway {
		t.Fatalf("stream error = %v, want 502 upstream error", err)
	}
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("failed upstream stream was marked successful: %s", recorder.Body.String())
	}
}

func TestStreamRequiresFinishAndDoneBeforeSuccess(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	recorder, err := callStreamResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("valid stream returned error: %v", err)
	}
	if got := strings.Count(recorder.Body.String(), "data: [DONE]"); got != 1 {
		t.Fatalf("client done marker count = %d, want exactly one; body=%s", got, recorder.Body.String())
	}
}

func TestStreamRejectsErrorOrMalformedEventAfterFinish(t *testing.T) {
	finish := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	for _, event := range []string{
		`data: {"error":{"message":"upstream failed","type":"server_error"}}`,
		`data: {broken json`,
	} {
		t.Run(event, func(t *testing.T) {
			recorder, err := callStreamResponse(strings.NewReader(finish + event + "\n\ndata: [DONE]\n\n"))
			if err == nil || err.StatusCode != http.StatusBadGateway {
				t.Fatalf("bad event was accepted: err=%v body=%s", err, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "[DONE]") {
				t.Fatal("failed stream was marked done")
			}
		})
	}
}
