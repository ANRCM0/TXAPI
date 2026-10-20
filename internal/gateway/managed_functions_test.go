package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
)

const managedFunctionTool = `{"type":"function","name":"weather","description":"fixture","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false},"strict":true}`
const managedFunctionCall = `{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"weather","arguments":"{\"city\":\"\u4e0a\u6d77\"}","status":"completed"}`
const managedFunctionResult = `{"type":"function_call_output","call_id":"call_fixture","output":"24 C"}`

func managedFunctionBody(input, extra string) string {
	return `{"model":"m","store":false,"stream":true,"input":` + input + extra + `}`
}

func managedFunctionFirstBody() string {
	return managedFunctionBody(`[{"role":"user","content":"weather?"}]`, `,"tools":[`+managedFunctionTool+`],"tool_choice":{"type":"function","name":"weather"},"parallel_tool_calls":false`)
}

func managedFunctionNextBody() string {
	return managedFunctionBody(`[{"role":"user","content":"weather?"},`+managedFunctionCall+`,`+managedFunctionResult+`]`, `,"tools":[`+managedFunctionTool+`],"tool_choice":"none"`)
}

func TestManagedFunctionClosedPermissionSurface(t *testing.T) {
	text := `[{"role":"user","content":"hello"}]`
	tools := `,"tools":[` + managedFunctionTool + `]`
	pair := `[` + managedFunctionCall + `,` + managedFunctionResult + `]`
	encode := func(v any) string { raw, _ := json.Marshal(v); return string(raw) }
	valid := []string{managedTestBody, managedFunctionFirstBody(), managedFunctionNextBody(),
		managedFunctionBody(text, `,"tools":[],"tool_choice":"none","parallel_tool_calls":true`),
		managedFunctionBody(text, tools+`,"tool_choice":"auto"`), managedFunctionBody(text, tools+`,"tool_choice":"required"`),
		managedFunctionBody(pair, ""),
		managedFunctionBody(strings.Replace(pair, `"24 C"`, encode(strings.Repeat("x", managedFunctionBytes)), 1), ""),
		managedFunctionBody(strings.Replace(pair, `"{\"city\":\"\u4e0a\u6d77\"}"`, encode(`{"x":"`+strings.Repeat("x", managedFunctionBytes-8)+`"}`), 1), ""),
		managedFunctionBody(text, `,"tools":[{"type":"function","name":"no_args"}]`),
		managedFunctionBody(text, strings.Replace(tools, `"function"`, `"funct\u0069on"`, 1)),
	}
	var definitions, calls, results []string
	for i := range 129 {
		definitions = append(definitions, fmt.Sprintf(`{"type":"function","name":"f_%d"}`, i))
		calls = append(calls, strings.ReplaceAll(managedFunctionCall, "call_fixture", fmt.Sprintf("call_%d", i)))
		results = append(results, strings.ReplaceAll(managedFunctionResult, "call_fixture", fmt.Sprintf("call_%d", i)))
	}
	valid = append(valid, managedFunctionBody(text, `,"tools":[`+strings.Join(definitions[:64], ",")+`]`),
		managedFunctionBody("["+strings.Join(append(append([]string{}, calls[:128]...), results[:128]...), ",")+"]", ""))
	for i, body := range valid {
		if err := validateManagedBody([]byte(body), "m"); err != nil {
			t.Fatalf("valid case %d refused: %v", i, err)
		}
	}
	invalid := []string{
		strings.Replace(managedFunctionFirstBody(), `"stream":true`, `"stream":true,"stream":true`, 1),
		managedFunctionBody(text, `,"tools":[{"type":"function","name":"weather","parameters":{"properties":{"x":{"type":"string","type":"number"}}}}]`),
		managedFunctionBody(text, `,"tools":null`), managedFunctionBody(text, `,"tools":{}`),
		managedFunctionBody(text, `,"tools":[null]`), managedFunctionBody(text, `,"tool_choice":null`),
		managedFunctionBody(text, `,"parallel_tool_calls":null`), managedFunctionBody(text, `,"parallel_tool_calls":1`),
		managedFunctionBody(text, `,"tool_choice":"required"`), managedFunctionBody(text, tools+`,"tool_choice":"invalid"`),
		managedFunctionBody(text, tools+`,"tool_choice":{"type":"function","name":"missing"}`),
		managedFunctionBody(text, tools+`,"tool_choice":{"type":"function","name":"weather","namespace":null}`),
		managedFunctionBody(text, `,"tools":[`+managedFunctionTool+`,`+managedFunctionTool+`]`),
		managedFunctionBody(text, `,"tools":[`+strings.Join(definitions[:65], ",")+`]`),
		managedFunctionBody("["+strings.Join(append(append([]string{}, calls...), results...), ",")+"]", ""),
		managedFunctionBody("["+managedFunctionCall+"]", ""), managedFunctionBody("["+managedFunctionResult+"]", ""),
		managedFunctionBody("["+managedFunctionResult+","+managedFunctionCall+"]", ""),
		managedFunctionBody("["+managedFunctionCall+","+managedFunctionCall+","+managedFunctionResult+"]", ""),
		managedFunctionBody("["+managedFunctionCall+","+managedFunctionResult+","+managedFunctionResult+"]", ""),
		managedFunctionBody(strings.Replace(pair, `"24 C"`, "null", 1), ""),
		managedFunctionBody(strings.Replace(pair, `"24 C"`, `[{"type":"input_text","text":"24 C"}]`, 1), ""),
		managedFunctionBody(strings.Replace(pair, `"24 C"`, encode(strings.Repeat("x", managedFunctionBytes+1)), 1), ""),
		managedFunctionBody(strings.Replace(pair, `"arguments":`, `"namespace":null,"arguments":`, 1), ""),
		managedFunctionBody(strings.Replace(pair, `"completed"`, `"in_progress"`, 1), ""),
		managedFunctionBody(strings.Replace(pair, `"completed"`, `null`, 1), ""),
		managedFunctionBody(strings.ReplaceAll(pair, `"call_fixture"`, `"call space"`), ""),
		managedFunctionBody(strings.ReplaceAll(pair, `"call_fixture"`, encode(strings.Repeat("c", 129))), ""),
		managedFunctionBody(strings.Replace(pair, `"fc_fixture"`, `""`, 1), ""),
	}
	for _, tool := range []string{
		`{"type":"namespace","name":"n","tools":[]}`, `{"type":"custom","name":"c"}`, `{"type":"tool_search","execution":"client"}`,
		`{"type":"web_search","external_web_access":false}`, `{"type":"function","name":"weather","namespace":null}`,
		`{"type":"function","name":"weather","defer_loading":false}`, `{"type":"function","name":"weather","parameters":null}`,
		`{"type":"function","name":"weather","parameters":[]}`, `{"type":"function","name":"weather","strict":null}`,
		`{"type":"function","name":"weather","description":null}`, `{"type":"function","name":"bad name"}`,
		`{"type":"function","name":` + encode(strings.Repeat("n", 65)) + `}`,
		`{"type":"function","name":"weather","parameters":{"x":"` + strings.Repeat("x", managedFunctionBytes) + `"}}`,
	} {
		invalid = append(invalid, managedFunctionBody(text, `,"tools":[`+tool+`]`))
	}
	for _, args := range []string{`null`, `""`, `"[]"`, `"{\"x\":1,\"x\":2}"`, `"{broken}"`, encode(`{"x":"` + strings.Repeat("x", managedFunctionBytes) + `"}`)} {
		invalid = append(invalid, managedFunctionBody(strings.Replace(pair, `"{\"city\":\"\u4e0a\u6d77\"}"`, args, 1), ""))
	}
	a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
	g := newManagedTestGateway(t, a, d, func(*http.Request) (*http.Response, error) {
		t.Fatal("unapproved function body reached the wire")
		return nil, nil
	})
	for i, body := range invalid {
		if err := validateManagedBody([]byte(body), "m"); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
		result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(body)})
		if result.StatusCode != 400 || result.Phase != core.NotDispatched || len(result.Attempts) != 0 || len(a.requests) != 0 || len(d.requests) != 0 {
			t.Fatalf("invalid case %d crossed credential boundary: %+v", i, result)
		}
	}
}

func TestManagedFunctionRoundsPreserveBodyAndReacquire(t *testing.T) {
	for _, denySecond := range []bool{false, true} {
		t.Run(fmt.Sprint(denySecond), func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			if denySecond {
				a.failAt = 3
			}
			hits := 0
			bodies := []string{managedFunctionFirstBody(), managedFunctionNextBody()}
			g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if hits >= 2 || string(body) != bodies[hits] || r.URL.String() != managedOrigin+"/responses" || r.Header.Get("Authorization") != "Bearer fixture-access-token" {
					t.Fatal("managed tools lost exact body, binding or target")
				}
				hits++
				wire := managedTestTerminal("completed")
				if hits == 1 {
					wire = "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"city\\\":\\\"\u4e0a\"}\n\n" +
						"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"\u6d77\\\"}\"}\n\n" +
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"completed\",\"output\":[" + managedFunctionCall + "],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})
			for i, body := range bodies {
				out := &observerOutput{}
				result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, ResponseID: fmt.Sprintf("public_%d", i), Body: []byte(body)})
				if i == 1 && denySecond {
					if result.Error == nil || result.ProtocolComplete || hits != 1 || result.Replayable {
						t.Fatal("second tool round reused a stale credential", result)
					}
					continue
				}
				if result.Error != nil || !result.ProtocolComplete || len(result.Attempts) != 1 || result.CredentialID != 90 || result.Replayable || strings.Contains(out.data.String(), "fixture-access-token") {
					t.Fatalf("native tool round failed: %+v body=%s", result, out.data.String())
				}
				if i == 0 && (!strings.Contains(out.data.String(), "call_fixture") || result.Usage == nil || result.Usage.TotalTokens != 3) {
					t.Fatal("function call or native usage changed", out.data.String(), result.Usage)
				}
			}
			if len(a.requests) != 3 || len(d.requests) != 1 {
				t.Fatalf("tool rounds skipped current authorization: issue=%d directory=%d", len(a.requests), len(d.requests))
			}
		})
	}
}

func TestManagedFunctionFailureFactsNeverReplay(t *testing.T) {
	for _, mode := range []string{"403", "429", "failed", "incomplete", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			hits := 0
			g := newManagedTestGateway(t, a, d, func(*http.Request) (*http.Response, error) {
				hits++
				status, wire := 200, managedTestTerminal(mode)
				if mode == "403" || mode == "429" {
					status = 403
					if mode == "429" {
						status = 429
					}
					wire = `{"error":{"message":"private-refresh-token"}}`
				} else if mode == "truncated" {
					wire = "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\"}\n\n"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})
			out := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, Body: []byte(managedFunctionFirstBody())})
			if hits != 1 || len(result.Attempts) != 1 || result.Replayable || result.Usage != nil || strings.Contains(out.data.String(), "response.completed") || strings.Contains(out.data.String(), "private-refresh-token") {
				t.Fatalf("function failure changed execution/usage: %+v body=%s", result, out.data.String())
			}
			terminal := mode == "failed" || mode == "incomplete"
			if result.ProtocolComplete != terminal || (!terminal && result.Error == nil) {
				t.Fatalf("function failure fabricated terminal state: %+v", result)
			}
		})
	}
}

func TestManagedFunctionCancellationReleasesSocketAndCapacity(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	var hits, acquired, released atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\"}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer upstream.Close()
	a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
	client := upstream.Client()
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: managedTestChannels(),
		ManagedInference: &ManagedInferenceOptions{Enabled: true, Credentials: a, DirectoryCaller: d, ValidateModel: openaiauth.ValidateVisibleModel, FailureRecorder: &managedRecorderFixture{}, ClassifyFailure: openaiauth.ClassifyInferenceFailure},
		Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
			acquired.Add(1)
			return func() { released.Add(1) }, nil
		}), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			copy.URL.Scheme, copy.URL.Host = "http", strings.TrimPrefix(upstream.URL, "http://")
			return client.Do(copy)
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithCancel(outputIdentity())
	defer cancel()
	done := make(chan RequestResult, 1)
	go func() {
		done <- g.ExecuteTo(ctx, &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(managedFunctionFirstBody())})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("function socket did not start")
	}
	cancel()
	select {
	case result := <-done:
		if (result.Termination != core.Canceled && result.Termination != core.ClientWriteError) || result.ProtocolComplete || result.Replayable || len(result.Attempts) != 1 || result.Attempts[0].Termination != core.Canceled {
			t.Fatalf("function cancellation lost execution facts: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("function cancellation did not join")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("function socket did not cancel")
	}
	if hits.Load() != 1 || acquired.Load() != 2 || released.Load() != 2 {
		t.Fatal("function capacity leaked or request replayed", hits.Load(), acquired.Load(), released.Load())
	}
}
