package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

const cachedSearchTool = `{"type":"web_search","external_web_access":false,"search_content_types":["text","image"]}`
const completedSearchCall = `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"TXAPI","sources":[{"type":"url","url":"https://example.com","title":"example"}]},"metadata":{"response_id":"business"}}`

func TestCachedWebSearchDeclarationScopeAndPreservation(t *testing.T) {
	body := []byte(`{"model":"m","store":false,"input":"hello","tools":[` + cachedSearchTool + `],"tool_choice":{"type":"web_search"}}`)
	r, err := ParseRequest(body)
	if err != nil || r.ValidateHTTP() != nil {
		t.Fatal(err, r.ValidateHTTP())
	}
	out, err := r.WithModel("upstream")
	if err != nil || !bytes.Contains(out, []byte(cachedSearchTool)) {
		t.Fatal("search declaration changed", string(out), err)
	}
	if validateTools(r.fields["tools"]) == nil {
		t.Fatal("additional tools gained hosted search")
	}
	for _, store := range []string{"", `,"store":true`, `,"store":null`} {
		r, err := ParseRequest([]byte(`{"model":"m","input":"x","tools":[` + cachedSearchTool + `]` + store + `}`))
		if err == nil && r.ValidateHTTP() == nil {
			t.Fatal("search accepted without explicit store=false")
		}
	}
	background := []byte(`{"model":"m","store":false,"background":true,"input":"x","tools":[` + cachedSearchTool + `]}`)
	if _, err := (BackgroundParser{}).ParseRequest(execution.Responses, background); err == nil {
		t.Fatal("background gained hosted search")
	}
	for _, tool := range []string{
		`{"type":"web_search"}`, `{"type":"web_search","external_web_access":true}`,
		`{"type":"web_search","external_web_access":null}`, `{"type":"web_search","external_web_access":"false"}`,
		`{"type":"web_search","external_web_access":false,"unknown":true}`,
		`{"type":"web_search","external_web_access":false,"search_content_types":[]}`,
		`{"type":"web_search","external_web_access":false,"search_content_types":["text","text"]}`,
		`{"type":"web_search","external_web_access":false,"search_content_types":["audio"]}`,
		`{"type":"web_search","external_web_access":false,"search_content_types":null}`,
	} {
		if validateToolsWithWebSearch(json.RawMessage("["+tool+"]"), true) == nil {
			t.Fatal("invalid declaration accepted", tool)
		}
	}
	for _, extra := range []string{`"tool_choice":{"type":"web_search"}`, `"tools":[{"type":"namespace","name":"n","tools":[` + cachedSearchTool + `]}]`} {
		r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":"x",` + extra + `}`))
		if err == nil && r.ValidateHTTP() == nil {
			t.Fatal("unsupported search scope accepted", extra)
		}
	}
}

func TestWebSearchSelfContainedCalls(t *testing.T) {
	for _, item := range []string{
		completedSearchCall,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"open_page","url":"https://example.com"}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"find_in_page","url":"https://example.com","pattern":"x"}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","queries":["one","two"]}}`,
	} {
		if err := validateWebSearchCall([]byte(item), true); err != nil {
			t.Fatal(item, err)
		}
		r, err := ParseRequest([]byte(`{"model":"m","store":false,"input":[` + item + `]}`))
		if err != nil || r.ValidateHTTP() != nil {
			t.Fatal(err, r.ValidateHTTP())
		}
	}
	for _, item := range []string{
		`{"type":"web_search_call","id":"w"}`,
		`{"type":"web_search_call","id":"w","status":"completed"}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search"}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"open_page"}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"find_in_page","url":"x"}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","query":1}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","query":"x","sources":[{"url":"x"}]}}`,
	} {
		if validateOutputToolItem([]byte(item)) == nil {
			t.Fatal("malformed output accepted", item)
		}
		if validateWebSearchCall([]byte(item), true) == nil {
			t.Fatal("malformed input accepted", item)
		}
	}
	progress := []byte(`{"type":"web_search_call","id":"w","status":"in_progress"}`)
	if validateOutputToolItem(progress) != nil || validateWebSearchCall(progress, true) == nil {
		t.Fatal("progress/output and self-contained input boundaries differ")
	}
}

func TestWebSearchNativeJSONAndSSE(t *testing.T) {
	response := `{"id":"up","object":"response","status":"completed","output":[` + completedSearchCall + `,{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.com","title":"example","start_index":0,"end_index":6}],"extension":{"response_id":"business"}}]}]}`
	out, usage, err := NewDecoderWithID("public").Decode([]byte(response))
	if err != nil || usage != nil || !bytes.Contains(out, []byte(completedSearchCall)) || !bytes.Contains(out, []byte(`"annotations":[{"type":"url_citation"`)) {
		t.Fatal(string(out), usage, err)
	}
	s := NewDecoder().NewStream().(*streamDecoder)
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"w","status":"in_progress"}}`,
		`{"type":"response.web_search_call.searching","item_id":"w","output_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + completedSearchCall + `}`,
		`{"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com"}}`,
		`{"type":"response.completed","response":` + response + `}`,
	} {
		if _, err := s.DecodeLine([]byte("data: " + event)); err != nil {
			t.Fatal(err)
		}
		frames, err := s.DecodeLine(nil)
		if err != nil || len(frames) != 1 {
			t.Fatal(err, frames)
		}
		if s.Progress().EffectiveOutput {
			t.Fatal("search lifecycle or annotations claimed effective output")
		}
	}
	if _, err := s.End(); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(response, `"query":"TXAPI"`, `"query":42`, 1)
	if _, _, err := NewDecoder().Decode([]byte(bad)); err == nil {
		t.Fatal("malformed JSON call accepted")
	}
	s = NewDecoder().NewStream().(*streamDecoder)
	_, _ = s.DecodeLine([]byte(`data: {"type":"response.output_item.done","item":` + strings.Replace(completedSearchCall, `"query":"TXAPI"`, `"query":42`, 1) + `}`))
	if _, err := s.DecodeLine(nil); err == nil {
		t.Fatal("malformed SSE call accepted")
	}
}

func TestWebSearchPartialUsageFailureAndTruncation(t *testing.T) {
	response := `{"id":"up","object":"response","status":"completed","output":[` + completedSearchCall + `],"usage":{"input_tokens":4}}`
	r, usage, err := ParseResponse([]byte(response))
	if err != nil || usage != nil || r.Usage == nil || r.Usage.Input == nil || *r.Usage.Input != 4 || r.Usage.Output != nil || r.Usage.Total != nil {
		t.Fatal("partial usage was discarded or completed", r.Usage, usage, err)
	}
	for _, terminal := range []string{"failed", "incomplete"} {
		s := NewDecoder().NewStream()
		event := `{"type":"response.` + terminal + `","response":{"id":"up","object":"response","status":"` + terminal + `","output":[{"type":"web_search_call","id":"w","status":"failed"}],"error":{"code":"upstream_error","message":"private"}}}`
		if _, err := s.DecodeLine([]byte("data: " + event)); err != nil {
			t.Fatal(err)
		}
		frames, err := s.DecodeLine(nil)
		if err != nil || len(frames) != 1 || !frames[0].Complete || frames[0].Usage != nil {
			t.Fatal(frames, err)
		}
		if _, err := s.End(); err != nil {
			t.Fatal(err)
		}
	}
	s := NewDecoder().NewStream()
	_, _ = s.DecodeLine([]byte(`data: {"type":"response.web_search_call.searching","item_id":"w","output_index":0}`))
	if _, err := s.DecodeLine(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End(); err == nil {
		t.Fatal("search progress concealed a truncated response")
	}
}

func TestWebSearchDuplicateAndSharedSurfaceRejections(t *testing.T) {
	duplicate := json.RawMessage("[" + cachedSearchTool + "," + cachedSearchTool + "]")
	if validateToolsWithWebSearch(duplicate, true) == nil {
		t.Fatal("duplicate hosted search declarations accepted")
	}
	background := []byte(`{"model":"m","background":true,"store":false,"input":[` + completedSearchCall + `]}`)
	if _, err := (BackgroundParser{}).ParseRequest(execution.Responses, background); err == nil {
		t.Fatal("self-contained search input without tools entered background")
	}
	loaded := []byte(`{"model":"m","store":false,"input":[{"type":"additional_tools","role":"developer","tools":[` + cachedSearchTool + `]}]}`)
	r, err := ParseRequest(loaded)
	if err == nil && r.ValidateHTTP() == nil {
		t.Fatal("additional_tools injected hosted search")
	}
	for _, item := range []string{
		`{"type":"web_search_call","id":null,"status":"in_progress"}`,
		`{"type":"web_search_call","id":"w","status":null}`,
		`{"type":"web_search_call","id":"w","status":"in_progress","action":null}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":null}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","query":null,"queries":["x"]}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","query":"x","queries":null}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"search","query":"x","sources":null}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"open_page","url":null}}`,
		`{"type":"web_search_call","id":"w","status":"completed","action":{"type":"find_in_page","url":"x","pattern":null}}`,
	} {
		if validateOutputToolItem([]byte(item)) == nil || validateWebSearchCall([]byte(item), true) == nil {
			t.Fatal("explicit null search field accepted", item)
		}
	}
}
