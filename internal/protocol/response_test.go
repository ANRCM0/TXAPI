package protocol

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minigateway/internal/core"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
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

type staticResponse struct{ body io.Reader }

func (s staticResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(s.body), Header: make(http.Header)}, nil
}
func callStreamResponse(body io.Reader) (*httptest.ResponseRecorder, *model.ErrorWithStatusCode) {
	rec := httptest.NewRecorder()
	e := core.New(core.Options{Client: &http.Client{Transport: staticResponse{body: body}}})
	result := e.Execute(context.Background(), rec, core.Input{URL: "http://upstream.test", Stream: true, Decoder: NewDecoder(&openai.Adaptor{}, &meta.Meta{IsStream: true})})
	return rec, result.Error
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
