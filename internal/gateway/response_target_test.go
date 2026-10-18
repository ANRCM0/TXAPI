package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"minigateway/internal/billing"
	"minigateway/internal/protocol"
)

func TestResponsesContinuationPinsOriginalCredentialAndRejectsChangedMappingOrSecret(t *testing.T) {
	hits := 0
	channels := lifecycleChannels("http://upstream.test")
	g, err := New(Options{BillingMode: billing.ModeDisabled, Channels: channels, RetryTimes: 2, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_native","object":"response","model":"m","status":"completed","output":[]}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	in := Execution{Operation: protocol.Responses, Body: []byte(`{"model":"m","input":"first"}`), ResponseID: "resp_local"}
	first := g.ExecuteTo(outputIdentity(), &observerOutput{}, in)
	if !first.ProtocolComplete || first.ResponseTarget.Fingerprint == "" {
		t.Fatalf("missing target provenance: %+v", first)
	}
	in.ResponseTarget = &first.ResponseTarget
	second := g.ExecuteTo(outputIdentity(), &observerOutput{}, in)
	if !second.ProtocolComplete || second.CredentialID != first.CredentialID || hits != 2 {
		t.Fatalf("target drift: %+v", second)
	}
	for i := range channels {
		for j := range channels[i].Credentials {
			if channels[i].Credentials[j].ID == first.CredentialID {
				channels[i].Credentials[j].APIKey = "rotated-key"
			}
		}
	}
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	changed := g.ExecuteTo(outputIdentity(), &observerOutput{}, in)
	if changed.StatusCode != 409 || hits != 2 {
		t.Fatalf("credential rotation silently switched: %+v", changed)
	}
	channels = lifecycleChannels("http://upstream.test")
	channels[0].ModelMapping = map[string]string{"m": "different-native-model"}
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	changed = g.ExecuteTo(outputIdentity(), &observerOutput{}, in)
	if changed.StatusCode != 409 || hits != 2 {
		t.Fatal("changed model mapping silently reused history")
	}
	in.ResponseTarget = nil
	if fresh := g.ExecuteTo(outputIdentity(), &observerOutput{}, in); !fresh.ProtocolComplete {
		t.Fatal("new generations unnecessarily blocked")
	}
	in.ResponseTarget = &first.ResponseTarget
	changed = g.ExecuteTo(outputIdentity(), &observerOutput{}, in)
	if changed.StatusCode != 409 || hits != 3 {
		t.Fatal("continuation dispatched with stale target")
	}
}
