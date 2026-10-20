package responses

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

const compactionFixture = `{"type":"compaction","id":"cmp_fixture","encrypted_content":"opaque-provider-bytes","metadata":{"response_id":"business","nested":{"error":"unchanged"}},"future":[1,2]}`

func TestCompactionHTTPInputAndRawPreservation(t *testing.T) {
	for _, input := range []string{
		`[{"role":"user","content":"question"},{"type":"compaction_trigger"}]`,
		`[` + compactionFixture + `,{"role":"user","content":"next"}]`,
		`[` + compactionFixture + `,` + compactionFixture + `,{"type":"compaction_trigger"}]`,
	} {
		body := []byte(`{"model":"alias","store":false,"input":` + input + `}`)
		r, err := ParseRequest(body)
		if err != nil || r.ValidateHTTP() != nil {
			t.Fatal(err, r.ValidateHTTP())
		}
		if r.HasCompactionTrigger() != strings.Contains(input, "compaction_trigger") {
			t.Fatal("incorrect trigger classification")
		}
		out, err := r.WithModel("native")
		var mapped map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &mapped) != nil {
			t.Fatal(err)
		}
		var before, after any
		_ = json.Unmarshal([]byte(input), &before)
		_ = json.Unmarshal(mapped["input"], &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("opaque input or metadata changed", string(out))
		}
		if strings.Contains(r.PromptText(), "opaque-provider-bytes") {
			t.Fatal("opaque bytes became user-text estimate")
		}
	}
	for _, input := range []string{
		`[{"type":"compaction_trigger"},{"role":"user","content":"late"}]`,
		`[{"type":"compaction_trigger"},{"type":"compaction_trigger"}]`,
		`[{"type":"compaction_trigger","id":"trigger"}]`,
		`[{"type":"compaction_trigger","encrypted_content":"x"}]`,
		`[{"type":"compaction","id":"bare"}]`,
		`[{"type":"compaction","encrypted_content":null}]`,
		`[{"type":"compaction","encrypted_content":""}]`,
		`[{"type":"compaction","encrypted_content":42}]`,
		`[{"type":"compaction","id":null,"encrypted_content":"x"}]`,
		`[{"type":"compaction","id":"","encrypted_content":"x"}]`,
	} {
		r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":` + input + `}`))
		if err == nil && r.ValidateHTTP() == nil {
			t.Fatal("invalid compact input accepted", input)
		}
	}
}

func TestCompactionScopeAndAutomaticManagementRejections(t *testing.T) {
	for _, input := range []string{`[{"type":"compaction_trigger"}]`, `[` + compactionFixture + `]`} {
		for _, store := range []string{"", `,"store":true`, `,"store":null`} {
			r, err := ParseRequest([]byte(`{"model":"m","input":` + input + store + `}`))
			if err == nil && r.ValidateHTTP() == nil {
				t.Fatal("compaction accepted without explicit store=false")
			}
		}
		body := []byte(`{"model":"m","background":true,"store":false,"input":` + input + `}`)
		if _, err := (BackgroundParser{}).ParseRequest(execution.Responses, body); err == nil {
			t.Fatal("compaction entered background")
		}
		for _, continuation := range []string{`"previous_response_id":"resp_parent"`, `"conversation":"conv_parent"`} {
			r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":` + input + `,` + continuation + `}`))
			if err == nil && r.ValidateHTTP() == nil {
				t.Fatal("compaction entered persisted continuation")
			}
		}
	}
	for _, management := range []string{`[]`, `{}`, `[{"type":"compaction"}]`, `false`, `"auto"`} {
		r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":"x","context_management":` + management + `}`))
		if err == nil && r.ValidateHTTP() == nil {
			t.Fatal("automatic context management entered native forwarding")
		}
	}
	r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":"x","metadata":{"type":"compaction_trigger"}}`))
	if err != nil || r.HasCompactionTrigger() {
		t.Fatal("metadata fabricated a compact trigger", err)
	}
}

func TestCompactionOutputStageAndOpaqueBounds(t *testing.T) {
	for _, item := range []string{compactionFixture, `{"type":"compaction","encrypted_content":"x"}`} {
		if err := validateOutputToolItem([]byte(item)); err != nil {
			t.Fatal(err)
		}
	}
	pending := []byte(`{"type":"compaction","id":"cmp_pending","future":{"keep":true}}`)
	if validateOutputToolItemComplete(pending, false) != nil || validateOutputToolItem(pending) == nil {
		t.Fatal("pending and complete output have identical opaque requirements")
	}
	for _, item := range []string{
		`{"type":"compaction_trigger"}`, `{"type":"compaction","encrypted_content":null}`,
		`{"type":"compaction","encrypted_content":""}`, `{"type":"compaction","encrypted_content":{}}`,
		`{"type":"compaction","id":false,"encrypted_content":"x"}`,
		`{"type":"compaction","id":"bad\nidentity","encrypted_content":"x"}`,
	} {
		if validateOutputToolItemComplete([]byte(item), false) == nil || validateOutputToolItem([]byte(item)) == nil {
			t.Fatal("invalid compact output accepted", item)
		}
	}
	for _, size := range []int{maxEventBytes, maxEventBytes + 1} {
		raw, _ := json.Marshal(map[string]any{"type": "compaction", "encrypted_content": strings.Repeat("x", size)})
		err := validateCompactionItem(raw, true)
		if (err == nil) != (size == maxEventBytes) {
			t.Fatal("incorrect opaque limit", size, err)
		}
	}
	oversizedID, _ := json.Marshal(map[string]any{"type": "compaction", "id": strings.Repeat("i", 257), "encrypted_content": "x"})
	if validateCompactionItem(oversizedID, true) == nil {
		t.Fatal("oversized compact id accepted")
	}
}
