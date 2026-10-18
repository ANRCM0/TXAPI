package responses_test

import (
	"bytes"
	"fmt"
	"minigateway/internal/model"
	"strings"
	"testing"
)

func TestHistoryRefusesUnreconstructableReasoningWithoutDroppingIt(t *testing.T) {
	f := newResponseFixture(t)
	e := &historyExecutor{output: `[{"id":"rs_native_hidden","type":"reasoning","summary":[]},{"id":"msg_visible","type":"message","role":"assistant","content":[{"type":"output_text","text":"visible answer"}]}]`}
	s := historyService(t, f, e, "old", map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)})
	first := historyCall(s, f.principal, "POST", "/v1/responses", `{"model":"m","input":"first"}`, "initial", "")
	id := objectID(t, first)
	get := historyCall(s, f.principal, "GET", "/v1/responses/"+id, "", "", "")
	if get.Code != 200 || !strings.Contains(get.Body.String(), "rs_native_hidden") {
		t.Fatal("native output was silently modified")
	}
	child := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"continue","previous_response_id":%q}`, id), "continue", "")
	if child.Code != 409 || !strings.Contains(child.Body.String(), "history_context_unavailable") || e.calls != 1 {
		t.Fatalf("incomplete hidden history executed: %d %s calls=%d", child.Code, child.Body, e.calls)
	}
	for _, storage := range []string{`true`, `false`} {
		w := historyCall(s, f.principal, "POST", "/v1/responses", `{"model":"m","input":[{"type":"reasoning","id":"rs_foreign"}],"store":`+storage+`}`, "", "")
		if w.Code != 400 || e.calls != 1 {
			t.Fatal("caller native item reference bypassed ownership")
		}
	}
}

func TestUnknownConversationEncryptedMarkerSurvivesBusyColumnTampering(t *testing.T) {
	f := newResponseFixture(t)
	e := &historyExecutor{unknown: true}
	s := historyService(t, f, e, "old", map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)})
	conv := objectID(t, historyCall(s, f.principal, "POST", "/v1/conversations", `{}`, "", ""))
	first := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"unknown","conversation":%q}`, conv), "unknown", "")
	if first.Code != 200 || e.calls != 1 {
		t.Fatal("fixture did not dispatch")
	}
	if err := f.st.DB().Model(&model.ResponseHistory{}).Where("id = ?", conv).Update("busy", "").Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"next", "another"} {
		w := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"must not execute","conversation":%q}`, conv), key, "")
		if w.Code != 409 || e.calls != 1 {
			t.Fatalf("altered busy column reopened unknown execution: %d %s calls=%d", w.Code, w.Body, e.calls)
		}
	}
}
