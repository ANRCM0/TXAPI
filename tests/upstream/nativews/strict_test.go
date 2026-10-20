package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStrictFixtureWireAndUsage(t *testing.T) {
	f := &strictFixture{jobs: make(map[string]map[string]any)}
	s := httptest.NewTLSServer(http.HandlerFunc(f.provider))
	defer s.Close()
	call := func(method, path, body string) map[string]any {
		t.Helper()
		r, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+upstreamKey)
		response, err := s.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("status %d", response.StatusCode)
		}
		var value map[string]any
		if err = json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, kind := range []string{"known", "zero", "missing"} {
		value := call("POST", "/v1/responses", `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"http-`+kind+`"}]}]}`)
		usage, ok := value["usage"].(map[string]any)
		if kind == "missing" {
			if ok {
				t.Fatal("missing usage became zero")
			}
			continue
		}
		input, output := float64(2), float64(1)
		if kind == "zero" {
			input, output = 0, 0
		}
		if !ok || usage["input_tokens"] != input || usage["output_tokens"] != output {
			t.Fatal(value)
		}
	}
	background := call("POST", "/v1/responses", `{"model":"m","input":"background-cancel","background":true}`)
	id := background["id"].(string)
	if background["status"] != "queued" {
		t.Fatal(background)
	}
	if call("GET", "/v1/responses/"+id, "")["status"] != "in_progress" {
		t.Fatal("poll completed cancellation fixture")
	}
	cancelled := call("POST", "/v1/responses/"+id+"/cancel", "")
	if _, ok := cancelled["usage"]; ok || cancelled["status"] != "cancelled" {
		t.Fatal(cancelled)
	}
	if call("GET", "/v1/responses/"+id, "")["status"] != "cancelled" {
		t.Fatal("cancel state not persistent")
	}
	if len(f.rows) != 7 {
		t.Fatal(f.rows)
	}
	request, _ := http.NewRequest("POST", s.URL+"/v1/responses", strings.NewReader(strings.Repeat("x", 4097)))
	request.Header.Set("Authorization", "Bearer "+upstreamKey)
	r, err := s.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 400 || len(f.rows) != 7 {
		t.Fatal("oversize fixture request accepted")
	}
}

func TestStrictFixtureWSSeparateRoundReceipts(t *testing.T) {
	f := &strictFixture{jobs: make(map[string]map[string]any)}
	s := httptest.NewTLSServer(http.HandlerFunc(f.provider))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	payload := `[{"type":"response.create","model":"m","input":"ws-known","max_output_tokens":32,"store":false},{"type":"response.create","model":"m","input":"ws-zero","max_output_tokens":32,"store":false}]`
	if err := strictWSCall(ctx, s.Client(), s.URL, upstreamKey, payload, &output); err != nil {
		t.Fatal(err)
	}
	if len(f.rows) != 2 || f.rows[0]["connection"] != f.rows[1]["connection"] {
		t.Fatal(f.rows)
	}
	if strings.Count(output.String(), `"response.completed"`) != 2 || !strings.Contains(output.String(), `"round":2`) {
		t.Fatal(output.String())
	}
}
