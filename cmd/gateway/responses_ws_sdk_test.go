package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/model"
)

// The optional SDK harness compiles this test binary and runs this helper in a
// separate process. It uses production assembly with the existing test-only
// upstream resource factory. The actual shipped image has separate coverage.
func TestResponsesWSPythonSDKProcess(t *testing.T) {
	directory := os.Getenv("TXAPI_TEST_WS_SDK_DIRECTORY")
	if directory == "" {
		t.Skip("only the bounded Python SDK harness starts this helper")
	}
	f := newWSAssemblyFixtureWithRecordLimit(t, true, true, 16)
	data, err := json.Marshal(map[string]string{"base": f.front.URL, "key": f.key})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ready.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "done")); err == nil {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("SDK client did not finish before fixture deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.waitReleased(t)
	if f.attempts.Load() != 10 || f.httpCalls.Load() != 0 {
		t.Fatalf("SDK must produce exactly ten WS creates, got %d/%d", f.attempts.Load(), f.httpCalls.Load())
	}
	var observations []wsAssemblyObservation
	for len(observations) < 10 {
		select {
		case observation := <-f.observations:
			observations = append(observations, observation)
		default:
			t.Fatal("missing wire observations")
		}
	}
	for index, expected := range []int{1, 3, 5, 7, 1, 1, 1, 1, 1, 3} {
		items := historyWire(t, observations[index], expected)
		if !observations[index].PrivateHeaders || observations[index].Key != "upstream-main-fixture" {
			t.Fatal("SDK credentials/private headers reached provider")
		}
		if index == 2 && (string(items[3]["call_id"]) != `"call_fixture"` || string(items[4]["call_id"]) != `"call_fixture"`) {
			t.Fatal("SDK function association lost")
		}
		if index == 9 && (string(items[1]["type"]) != `"custom_tool_call"` || string(items[2]["type"]) != `"custom_tool_call_output"` || string(items[1]["namespace"]) != `"text"` || string(items[2]["namespace"]) != `"text"` || string(items[1]["call_id"]) != string(items[2]["call_id"])) {
			t.Fatal("SDK custom association lost")
		}
	}
	if observations[0].Connection != observations[1].Connection || observations[1].Connection != observations[2].Connection || observations[2].Connection == observations[3].Connection {
		t.Fatal("SDK same-connection rounds or explicit reconnect differed")
	}
	var rows []model.ResponseHistory
	if err := f.st.DB().Find(&rows).Error; err != nil || len(rows) != 10 {
		t.Fatal("SDK history count", len(rows), err)
	}
	unknown := 0
	for _, row := range rows {
		if row.Busy == "uncertain" {
			unknown++
		} else if row.Busy != "" {
			t.Fatal("SDK left active history")
		}
	}
	if unknown != 1 {
		t.Fatal("SDK canceled request did not retain uncertainty", unknown)
	}
	var usage []model.UsageInvocation
	if err := f.st.DB().Find(&usage).Error; err != nil || len(usage) != 10 {
		t.Fatal("SDK usage count", len(usage), err)
	}
	statuses := map[string]int{}
	for _, row := range usage {
		if row.State != "sealed" || row.Transport != "websocket" || row.AttemptCount != 1 {
			t.Fatalf("SDK did not finish one isolated invocation: %+v", row)
		}
		statuses[row.Host.ProtocolStatus]++
	}
	if statuses["completed"] != 7 || statuses["failed"] != 1 || statuses["incomplete"] != 1 {
		t.Fatal("SDK protocol outcomes changed", statuses)
	}
	var attempts []model.UsageAttemptReceipt
	if err := f.st.DB().Find(&attempts).Error; err != nil || len(attempts) != 10 {
		t.Fatal("SDK attempt count", len(attempts), err)
	}
	known := 0
	for _, attempt := range attempts {
		if !attempt.MayDispatch {
			t.Fatal("sent SDK invocation lost its dispatch fact")
		}
		if attempt.Host.ProtocolStatus == "completed" {
			if attempt.TotalTokens == nil || *attempt.TotalTokens != 3 {
				t.Fatal("SDK known usage lost")
			}
			known++
		} else if attempt.InputTokens != nil || attempt.OutputTokens != nil || attempt.TotalTokens != nil {
			t.Fatal("SDK unknown usage became a fabricated count")
		}
	}
	if known != 7 {
		t.Fatal("SDK known usage count", known)
	}
}
