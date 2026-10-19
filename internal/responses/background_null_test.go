package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"minigateway/internal/compat/httpoutput"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	app "minigateway/internal/foundation/contracts/responses/v1"
)

type backgroundDocumentDelivery struct {
	execution.DeadlineOutput
	problem app.Problem
}

func (*backgroundDocumentDelivery) Annotate(app.Metadata)   {}
func (d *backgroundDocumentDelivery) Problem(p app.Problem) { d.problem = p }
func (*backgroundDocumentDelivery) Document(app.Document)   {}

func TestBackgroundAbsentDocumentRendersObjectAndPreservesRealDocument(t *testing.T) {
	for _, content := range [][]byte{nil, []byte("null"), []byte(" \nnull\t"), []byte(" \t\n")} {
		for _, stage := range []bg.Stage{bg.StageDispatching, bg.StageTerminal} {
			out := httptest.NewRecorder()
			delivery := &backgroundDocumentDelivery{DeadlineOutput: httpoutput.New(out)}
			deliverBackground(context.Background(), delivery, bg.Receipt{ID: "resp_fixture", Document: content,
				Stage: stage, Model: "m", CreatedAt: time.Now(), ContentExpiresAt: time.Now().Add(time.Minute)})
			wantStatus := "queued"
			if stage == bg.StageTerminal {
				wantStatus = "failed"
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(out.Body.Bytes(), &object); err != nil || object == nil || string(object["status"]) != `"`+wantStatus+`"` || string(object["object"]) != `"response"` ||
				out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" || delivery.problem.Code != "" {
				t.Errorf("absent document %q at %s must render response object: %d %s problem=%+v decode=%v", content, stage, out.Code, out.Body.String(), delivery.problem, err)
			}
		}
	}
	document := []byte(" {\"id\":\"resp_fixture\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}\n")
	out := httptest.NewRecorder()
	delivery := &backgroundDocumentDelivery{DeadlineOutput: httpoutput.New(out)}
	deliverBackground(context.Background(), delivery, bg.Receipt{ID: "resp_fixture", Document: document, Stage: bg.StageTerminal, ContentExpiresAt: time.Now().Add(time.Minute)})
	if !bytes.Equal(out.Body.Bytes(), document) {
		t.Fatal("real document bytes changed", out.Body.String())
	}
}
