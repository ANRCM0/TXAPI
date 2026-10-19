package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRawExchangePreservesFormAndFailureWire(t *testing.T) {
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("wire method/content type: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "grant_type=refresh_token&refresh_token=fixture" {
			t.Errorf("form body = %q", body)
		}
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	executor := New(Options{MaxResponseBytes: 128})
	defer executor.Close()
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	result := executor.ExecuteRaw(context.Background(), RawInput{Method: http.MethodPost,
		URL: server.URL, Header: header, Body: []byte("grant_type=refresh_token&refresh_token=fixture")})
	if result.Phase != ResponseReceived || result.StatusCode != 400 || !result.Complete || result.Termination != Success ||
		string(result.Body) != `{"error":"invalid_grant"}` || result.Header.Get("Retry-After") != "30" || sent.Load() != 1 {
		t.Fatalf("wire facts lost: %+v, sends=%d", result, sent.Load())
	}
	if len(header) != 1 || header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatal("input header mutated")
	}
}

func TestRawExchangeBodyFailureIsNotComplete(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.ReadCloser
	}{
		{"overflow", io.NopCloser(strings.NewReader(strings.Repeat("x", 65)))},
		{"partial", io.NopCloser(io.MultiReader(strings.NewReader("part"), rawReadError{}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := New(Options{MaxResponseBytes: 64, Client: &http.Client{Transport: rawRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.GetBody != nil {
					t.Error("raw input received an automatic replay hook")
				}
				return &http.Response{StatusCode: 200, Body: tc.body, Header: make(http.Header)}, nil
			})}})
			defer executor.Close()
			result := executor.ExecuteRaw(context.Background(), RawInput{Method: "POST", URL: "https://example.invalid/token", Body: []byte("data")})
			if result.Phase != ResponseReceived || result.StatusCode != 200 || result.Complete || len(result.Body) != 0 || result.Termination != UpstreamError {
				t.Fatalf("incomplete body accepted: %+v", result)
			}
		})
	}
}

func TestRawExchangeCancellationAndTransportFacts(t *testing.T) {
	var calls atomic.Int64
	executor := New(Options{Client: &http.Client{Transport: rawRoundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("sensitive transport detail must not escape")
	})}})
	defer executor.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := executor.ExecuteRaw(ctx, RawInput{Method: "POST", URL: "https://example.invalid/token"})
	if result.Phase != NotDispatched || result.Termination != Canceled || calls.Load() != 0 {
		t.Fatalf("canceled input dispatched: %+v calls=%d", result, calls.Load())
	}
	result = executor.ExecuteRaw(context.Background(), RawInput{Method: "POST", URL: "https://example.invalid/token"})
	if result.Phase != Dispatching || result.Complete || result.StatusCode != 0 || calls.Load() != 1 {
		t.Fatalf("transport failure marked unsent/complete: %+v calls=%d", result, calls.Load())
	}
}

func TestRawExchangeDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	defer server.Close()
	executor := New(Options{})
	defer executor.Close()
	result := executor.ExecuteRaw(context.Background(), RawInput{Method: "POST", URL: server.URL, Body: []byte("private")})
	if result.StatusCode != 307 || !result.Complete || targetCalls.Load() != 0 {
		t.Fatalf("redirect followed or lost: %+v target=%d", result, targetCalls.Load())
	}
}

type rawRoundTrip func(*http.Request) (*http.Response, error)

func (f rawRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type rawReadError struct{}

func (rawReadError) Read([]byte) (int, error) { return 0, errors.New("read interrupted") }
