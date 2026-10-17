package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
)

type mutatingRequestAdaptor struct {
	openai.Adaptor
	bodies []string
}

func (a *mutatingRequestAdaptor) ConvertRequest(body []byte, _ *meta.Meta) ([]byte, error) {
	a.bodies = append(a.bodies, string(body))
	body[len(body)-1] = ' '
	return []byte(`{"model":"mapped"}`), nil
}

func TestOperationEnvelopeSurvivesConversionAndRetries(t *testing.T) {
	a := &mutatingRequestAdaptor{}
	var hits int
	var scopes []AdmissionScope
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), RetryTimes: 1, BillingMode: billing.ModeDisabled,
		Adaptors: map[string]adaptor.Adaptor{"openai": a},
		Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
			scopes = append(scopes, scope)
			return func() {}, nil
		}), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hits++
			status, body := 200, `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
			if hits == 1 {
				status, body = 500, `{"error":{"message":"retry"}}`
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"keep"}],"extension":{"keep":true}}`)
	original := string(body)
	result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Chat, Body: body})
	if result.StatusCode != 200 || hits != 2 || len(result.Attempts) != 2 || result.Operation != protocol.Chat || string(body) != original || !reflect.DeepEqual(a.bodies, []string{original, original}) {
		t.Fatalf("operation/body drift: %+v bodies=%v", result, a.bodies)
	}
	for _, attempt := range result.Attempts {
		if attempt.Operation != protocol.Chat {
			t.Fatalf("ambiguous attempt: %+v", attempt)
		}
	}
	if len(scopes) != 3 {
		t.Fatalf("split admission lifecycle: %+v", scopes)
	}
	for _, scope := range scopes {
		if scope.Operation != protocol.Chat {
			t.Fatalf("operation missing from admission: %+v", scope)
		}
	}
}

func TestLegacyRelayCannotSubstituteRoutingFactsOrUnknownMode(t *testing.T) {
	g := testCoordinator(t)
	if err := g.initRegistry(lifecycleChannels("http://upstream.test")); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []int{meta.ModeChatCompletions, 99} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"other"}`), nil, mode, time.Second)
		if rec.Code != 400 || len(state(c).Attempts) != 0 {
			t.Fatalf("metadata bypass: %+v", state(c))
		}
	}
}
