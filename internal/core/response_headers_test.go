package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type headerOrderBody struct {
	io.Reader
	t        *testing.T
	observed *int
	closed   bool
}

func (b *headerOrderBody) Read(p []byte) (int, error) {
	if *b.observed != 1 {
		b.t.Fatal("response body read before its single header observation")
	}
	return b.Reader.Read(p)
}
func (b *headerOrderBody) Close() error { b.closed = true; return nil }

func TestResponseHeadersObservedOnceBeforeAnyBodyOrCommit(t *testing.T) {
	for _, status := range []int{200, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			body := &headerOrderBody{Reader: strings.NewReader(`{"error":{"message":"bounded error"}}`), t: t, observed: &calls}
			output := httptest.NewRecorder()
			e := New(Options{Client: &http.Client{Transport: rawRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"req-test"}}, Body: body}, nil
			})}})
			defer e.Close()
			result := e.Execute(context.Background(), output, Input{URL: "http://upstream.test", Decoder: rawDecoder{},
				ObserveResponseHeaders: func(code int, headers http.Header) {
					calls++
					if code != status || headers.Get("X-Request-Id") != "req-test" || output.Body.Len() != 0 || output.Flushed {
						t.Fatal("header observation changed response or followed output")
					}
				}})
			if calls != 1 || !body.closed || result.UpstreamStatus != status || result.Phase != ResponseReceived {
				t.Fatalf("observation/close facts: calls=%d closed=%v result=%+v", calls, body.closed, result)
			}
		})
	}
}

func TestResponseHeadersNotObservedWithoutResponse(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		calls, sends := 0, 0
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		e := New(Options{Client: &http.Client{Transport: rawRoundTrip(func(*http.Request) (*http.Response, error) {
			sends++
			return nil, errors.New("no response")
		})}})
		result := e.Execute(ctx, httptest.NewRecorder(), Input{URL: "http://upstream.test", Decoder: rawDecoder{},
			ObserveResponseHeaders: func(int, http.Header) { calls++ }})
		cancel()
		e.Close()
		if calls != 0 || result.Error == nil || canceled && sends != 0 || !canceled && sends != 1 {
			t.Fatalf("unexpected observation/send: canceled=%v calls=%d sends=%d result=%+v", canceled, calls, sends, result)
		}
	}
}
