package responses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func TestHistoryToolsOpaqueItemsPermissionChangesTamperingAndTTL(t *testing.T) {
	f := newResponseFixture(t)
	e := &historyExecutor{output: `[{"id":"rs_private","type":"reasoning","encrypted_content":"opaque-provider-reasoning","summary":[]},{"id":"fc_private","type":"function_call","call_id":"call_one","name":"lookup","arguments":"{\"x\":1}"}]`}
	keys := map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)}
	s := historyService(t, f, e, "old", keys)
	first := historyCall(s, f.principal, "POST", "/v1/responses", `{"model":"m","input":"tool question","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, "tools", "")
	id := objectID(t, first)
	e.output = ""
	child := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":"call_one","output":"lookup result"}]}`, id), "tool-result", "")
	childID := objectID(t, child)
	if !bytes.Contains(e.bodies[1], []byte("opaque-provider-reasoning")) || !bytes.Contains(e.bodies[1], []byte("lookup result")) || !bytes.Contains(e.bodies[1], []byte("call_one")) {
		t.Fatal("opaque/tool chain lost")
	}
	var row model.ResponseHistory
	if err := f.st.DB().First(&row, "id = ?", childID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Model(&model.Token{}).Where("id = ?", f.binding.TokenID).Updates(map[string]any{"model_mode": access.Allowlist, "models": "[\"other\"]"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := historyCall(s, f.principal, "GET", "/v1/responses/"+childID, "", "", ""); w.Code != 404 {
		t.Fatalf("revoked model leaked: %d %s", w.Code, w.Body)
	}
	if err := f.st.DB().Model(&model.Token{}).Where("id = ?", f.binding.TokenID).Updates(map[string]any{"model_mode": access.All}).Error; err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(row.Ciphertext)
	bad[len(bad)-1] ^= 1
	if err := f.st.DB().Model(&row).Update("ciphertext", bad).Error; err != nil {
		t.Fatal(err)
	}
	if w := historyCall(s, f.principal, "GET", "/v1/responses/"+childID, "", "", ""); w.Code != 503 || strings.Contains(w.Body.String(), "lookup result") {
		t.Fatal("tampered content exposed")
	}
	if err := f.st.DB().Model(&row).Updates(map[string]any{"ciphertext": row.Ciphertext, "expires_at": time.Now().Add(-time.Second).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if w := historyCall(s, f.principal, "GET", "/v1/responses/"+childID, "", "", ""); w.Code != 404 {
		t.Fatal("expired content exposed")
	}
	if n, err := f.st.CleanupResponseHistory(context.Background(), 200); err != nil || n != 1 {
		t.Fatalf("expiry cleanup: %d %v", n, err)
	}
	if e.calls != 2 {
		t.Fatal("reads bypassed generation isolation")
	}
	// Wrong owner cannot even discover valid IDs; current auth is checked too.
	owner, err := f.st.CreateUser(&store.UserInput{Username: "other-history-owner", Password: "other-password-123!", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.st.IssueToken(owner.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.st.ResolvePrincipal(context.Background(), access.Binding{UserID: owner.ID, TokenID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/responses/" + id, "/v1/responses/resp_nonexistent"} {
		if w := historyCall(s, p, "GET", path, "", "", ""); w.Code != 404 {
			t.Fatal("cross owner enumeration")
		}
	}
}

func TestHistoryUnknownPanicCapacityAndMetadataContracts(t *testing.T) {
	f := newResponseFixture(t)
	e := &historyExecutor{}
	s := historyService(t, f, e, "old", map[string][]byte{"old": bytes.Repeat([]byte{31}, 32)})
	for _, body := range []string{`{"metadata":{"key":123}}`, `{"metadata":{},"metadata":{}}`, `{"unknown":true}`, `{"items":[{"type":"item_reference","id":"foreign"}]}`} {
		if w := historyCall(s, f.principal, "POST", "/v1/conversations", body, "", ""); w.Code != 400 {
			t.Fatalf("invalid create accepted %s: %d", body, w.Code)
		}
	}
	id := objectID(t, historyCall(s, f.principal, "POST", "/v1/conversations", `{}`, "", ""))
	for _, body := range []string{fmt.Sprintf(`{"model":"m","input":"x","store":false,"conversation":%q}`, id), fmt.Sprintf(`{"model":"m","input":"x","conversation":%q,"previous_response_id":"resp_parent"}`, id)} {
		if w := historyCall(s, f.principal, "POST", "/v1/responses", body, "", ""); w.Code != 400 {
			t.Fatalf("invalid reference accepted: %d %s", w.Code, w.Body)
		}
	}
	large, _ := json.Marshal(map[string]any{"model": "m", "input": strings.Repeat("x", 260<<10)})
	if w := historyCall(s, f.principal, "POST", "/v1/responses", string(large), "too-large", ""); w.Code != 429 || e.calls != 0 {
		t.Fatal("expanded history capacity not checked before dispatch")
	}
	e.panicNow = true
	panicResult := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"interrupt","conversation":%q}`, id), "panic", "")
	if panicResult.Code != 500 {
		t.Fatal(panicResult.Body)
	}
	if err := f.st.RecoverResponseHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.panicNow = false
	if w := historyCall(s, f.principal, "POST", "/v1/responses", fmt.Sprintf(`{"model":"m","input":"unsafe","conversation":%q}`, id), "unsafe-panic", ""); w.Code != 409 {
		t.Fatal("interrupted generation was reopened")
	}
	if w := historyCall(s, f.principal, "GET", "/v1/conversations/"+id+"/items?after=missing", "", "", ""); w.Code != 400 {
		t.Fatal("unknown cursor accepted")
	}
	if e.calls != 1 {
		t.Fatal("unknown execution retried")
	}
}
