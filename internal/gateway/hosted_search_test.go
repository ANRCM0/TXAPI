package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

func TestHostedSearchBillingBoundaryBeforeAdmission(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeDisabled, billing.ModeLegacy, billing.ModeStrict} {
		for _, fields := range []string{
			`"input":"hello","tools":[{"type":"web_search","external_web_access":false,"search_content_types":["text","image"]}]`,
			`"input":[{"type":"web_search_call","id":"ws_fixture","status":"completed","action":{"type":"search","query":"fixture"}}]`,
		} {
			t.Run(string(mode)+fields[:7], func(t *testing.T) {
				hits, admissions := 0, 0
				g, err := newRegisteredGateway(Options{BillingMode: mode, Channels: lifecycleChannels("http://fixture.test"),
					Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
						admissions++
						return func() {}, nil
					}), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						hits++
						body, err := io.ReadAll(r.Body)
						if err != nil || !strings.Contains(string(body), "web_search") {
							t.Error("hosted search body lost", err)
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_remote","object":"response","status":"completed","output":[],"usage":{"input_tokens":1}}`))}, nil
					})}})
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				out := &observerOutput{}
				result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, Body: []byte(`{"model":"m","store":false,` + fields + `}`)})
				if mode == billing.ModeDisabled {
					if result.StatusCode != 200 || hits != 1 || admissions != 2 || !result.ProtocolComplete || result.Usage != nil {
						t.Fatalf("disabled search failed or invented usage: %+v hits=%d admissions=%d", result, hits, admissions)
					}
					var response map[string]any
					if json.Unmarshal([]byte(out.data.String()), &response) != nil {
						t.Fatal("invalid native output")
					}
				} else if result.StatusCode != 400 || result.Error == nil || result.Error.Error.Type != "unsupported_billing_dimension" || result.Phase != core.NotDispatched || hits != 0 || admissions != 0 || len(result.Attempts) != 0 {
					t.Fatalf("unpriced search reached execution: %+v hits=%d admissions=%d", result, hits, admissions)
				}
			})
		}
	}
}
