package main

import (
	"net/http"
	"strings"
	"testing"

	ws "github.com/coder/websocket"
)

func TestResponsesWSProductRoundAndContinuation(t *testing.T) {
	f := newWSAssemblyFixture(t)
	c := f.dial(t)
	f.send(t, c, "", "hello", false)
	first, kinds := f.receiveRound(t, c)
	if strings.Join(kinds, ",") != "response.created,response.output_text.delta,response.completed" {
		t.Fatal(kinds)
	}
	a := <-f.observations
	f.send(t, c, first, "lookup", true)
	second, kinds := f.receiveRound(t, c)
	if second == first || strings.Join(kinds, ",") != "response.created,response.output_item.added,response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done,response.completed" {
		t.Fatal(kinds)
	}
	b := <-f.observations
	if a.Connection != b.Connection || b.Previous != first || a.Model != "m" || b.Model != "m" || a.Key != "upstream-main-fixture" || !a.PrivateHeaders || !b.PrivateHeaders {
		t.Fatal("native target, continuation or private headers changed")
	}
	if f.attempts.Load() != 2 || f.connections.Load() != 1 || f.httpCalls.Load() != 0 {
		t.Fatal("unexpected network attempt or HTTP fallback")
	}
}

func TestResponsesWSHandshakeAuthOriginAndConnectionCapacity(t *testing.T) {
	f := newWSAssemblyFixture(t)
	url := "ws" + strings.TrimPrefix(f.front.URL, "http") + "/v1/responses"
	for _, tc := range []struct {
		suffix string
		header http.Header
		status int
	}{
		{"", nil, 401},
		{"", http.Header{"Cookie": {"token=" + f.key}}, 401},
		{"?api_key=" + f.key, nil, 401},
		{"?api_key=ignored", http.Header{"Authorization": {"Bearer " + f.key}}, 400},
		{"", http.Header{"Authorization": {"Bearer " + f.key}, "Origin": {"https://evil.example"}}, 403},
	} {
		peer, response, err := ws.Dial(f.ctx, url+tc.suffix, &ws.DialOptions{HTTPHeader: tc.header})
		if peer != nil {
			_ = peer.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Fatalf("handshake status: want %d", tc.status)
		}
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}
	_ = f.dial(t)
	_ = f.dial(t)
	peer, response, err := ws.Dial(f.ctx, url, &ws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + f.key}}})
	if peer != nil {
		_ = peer.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != 429 {
		t.Fatal("per-key connection ceiling bypassed")
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if f.attempts.Load() != 0 {
		t.Fatal("handshake opened an upstream generation")
	}
}
