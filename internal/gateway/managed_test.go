package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
)

const managedTestBody = `{"model":"m","stream":true,"store":false,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`

type managedAccessFixture struct {
	requests []authv2.AccessRequest
	failAt   int
}

func (f *managedAccessFixture) AcquireAccess(_ context.Context, request authv2.AccessRequest) (authv2.AccessCredential, error) {
	f.requests = append(f.requests, request)
	if f.failAt != 0 && len(f.requests) >= f.failAt {
		return authv2.AccessCredential{}, errors.New("private-refresh-token")
	}
	return authv2.AccessCredential{AccountID: request.AccountID, Revision: request.ExpectedRevision, CredentialRevision: request.ExpectedCredentialRevision, AccessToken: auth.NewSecret("fixture-access-token"), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type managedDirectoryFixture struct {
	requests []auth.Request
	call     func(context.Context, auth.Request) auth.Response
}

func (f *managedDirectoryFixture) Call(ctx context.Context, request auth.Request) auth.Response {
	f.requests = append(f.requests, request)
	if f.call != nil {
		return f.call(ctx, request)
	}
	return auth.Response{Complete: true, Status: 200, Body: auth.NewSecret(`{"models":[{"slug":"m","visibility":"list"}]}`)}
}
func (*managedDirectoryFixture) Close() {}

func managedTestChannels() []model.Channel {
	channels := lifecycleChannels("http://static.test")
	channels[0].Priority = 100 // The matching managed grant overrides static priority.
	channels = append(channels, model.Channel{ID: 9, Name: "managed", Type: "openai_siwc", BaseURL: managedOrigin, Status: model.StatusEnabled, Weight: 1, Models: model.StringList{"m"}, Credentials: []model.Credential{{ID: 90, ChannelID: 9, Source: model.CredentialSourceManaged, AccountID: "account", BindingOwnerID: 1, BindingTokenID: 1, BindingModel: "m", BindingRevision: 2, AccountRevision: 3, CredentialRevision: 4, Status: model.CredentialActive, Weight: 1}}})
	return channels
}

func managedTestTerminal(status string) string {
	return "event: response." + status + "\ndata: {\"type\":\"response." + status + "\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"" + status + "\",\"output\":[]}}\n\n"
}

func newManagedTestGateway(t *testing.T, accessFixture *managedAccessFixture, directory *managedDirectoryFixture, response func(*http.Request) (*http.Response, error)) *Gateway {
	t.Helper()
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: managedTestChannels(),
		ManagedInference: &ManagedInferenceOptions{Enabled: true, Credentials: accessFixture, DirectoryCaller: directory, ValidateModel: openaiauth.ValidateVisibleModel},
		Client:           &http.Client{Transport: roundTripFunc(response)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func TestManagedExactGrantUsesOneExecutorAndMetadataOnlyCatalogCache(t *testing.T) {
	a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
	hits := 0
	g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
		hits++
		if r.URL.String() != managedOrigin+"/responses" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-access-token" || r.Header.Get("Idempotency-Key") != "" {
			t.Fatalf("managed request target/auth changed: %s", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != managedTestBody {
			t.Fatalf("request altered: %s", body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(managedTestTerminal("completed")))}, nil
	})
	for i := range 2 {
		out := &observerOutput{}
		result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, ResponseID: "public", Body: []byte(managedTestBody)})
		if result.Error != nil || result.StatusCode != 200 || !result.ProtocolComplete || result.Replayable || result.CredentialID != 90 || len(result.Attempts) != 1 || hits != i+1 || strings.Contains(out.data.String(), "native") || strings.Contains(out.data.String(), "[DONE]") {
			t.Fatalf("managed consumer failed: %+v body=%s", result, out.data.String())
		}
	}
	if len(d.requests) != 1 || len(a.requests) != 3 || d.requests[0].URL != managedOrigin+"/models" || d.requests[0].Method != "GET" || d.requests[0].Header.Get("Authorization") != "Bearer fixture-access-token" {
		t.Fatalf("catalog cache or issue count differs: directory=%d issue=%d", len(d.requests), len(a.requests))
	}
	for _, request := range a.requests {
		if request != (authv2.AccessRequest{CredentialID: 90, ExpectedBindingRevision: 2, AccountID: "account", ExpectedRevision: 3, ExpectedCredentialRevision: 4, OwnerID: 1, TokenID: 1, Model: "m"}) {
			t.Fatalf("untrusted or stale grant: %+v", request)
		}
	}
	for _, ch := range g.reg.allChannels() {
		for _, cred := range ch.credentials {
			if isManaged(&cred) && cred.APIKey != "" {
				t.Fatal("token persisted into routing snapshot")
			}
		}
	}
}

func TestManagedForbiddenFieldsRefuseBeforeAnyWireCall(t *testing.T) {
	for _, extra := range []string{`"background":false`, `"previous_response_id":null`, `"conversation":null`, `"tools":[]`, `"tool_choice":"none"`, `"temperature":1`, `"max_output_tokens":16`, `"metadata":{}`, `"service_tier":"default"`, `"stream_options":{}`} {
		t.Run(extra, func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			g := newManagedTestGateway(t, a, d, func(*http.Request) (*http.Response, error) { t.Fatal("forbidden request sent"); return nil, nil })
			body := strings.TrimSuffix(managedTestBody, "}") + "," + extra + "}"
			result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(body)})
			if result.StatusCode != 400 || result.Phase != core.NotDispatched || len(a.requests) != 0 || len(d.requests) != 0 {
				t.Fatalf("forbidden fields reached credential/wire boundary: %+v", result)
			}
		})
	}
}

func TestManagedFailuresNeverFallbackAndSanitizeProviderErrors(t *testing.T) {
	for _, mode := range []string{"403", "429", "failed", "incomplete", "truncated", "issue", "directory", "disable during directory"} {
		t.Run(mode, func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			if mode == "issue" {
				a.failAt = 1
			}
			if mode == "directory" {
				d.call = func(context.Context, auth.Request) auth.Response { return auth.Response{Complete: true, Status: 403} }
			}
			if mode == "disable during directory" {
				a.failAt = 2
			}
			hits := 0
			g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
				hits++
				if r.URL.String() != managedOrigin+"/responses" {
					t.Fatal("managed failure fell back to static")
				}
				status, wire := 200, managedTestTerminal(mode)
				if mode == "403" {
					status = 403
				}
				if mode == "429" {
					status = 429
				}
				if status != 200 {
					wire = `{"error":{"message":"fixture-access-token private-refresh-token id-token"}}`
				}
				if mode == "truncated" {
					wire = `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})
			out := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, Body: []byte(managedTestBody)})
			if result.Error == nil || result.StatusCode == 200 || hits > 1 || len(result.Attempts) != 1 || result.Replayable {
				t.Fatalf("failure became success/retried: %+v hits=%d", result, hits)
			}
			if (mode == "issue" || mode == "directory" || mode == "disable during directory") && hits != 0 {
				t.Fatal("preflight failure sent inference")
			}
			for _, secret := range []string{"fixture-access-token", "private-refresh-token", "id-token"} {
				if strings.Contains(result.ErrorMessage, secret) || strings.Contains(out.data.String(), secret) {
					t.Fatal("upstream credential leaked")
				}
			}
		})
	}
}

func TestManagedGrantFiltersModelsAndKeepsOtherStaticRoutesIndependent(t *testing.T) {
	a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
	hits := 0
	g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
		hits++
		if r.URL.Host != "static.test" {
			t.Fatal("nonmatching key got managed source")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(managedTestTerminal("completed")))}, nil
	})
	other := access.WithPrincipal(context.Background(), access.Principal{UserID: 2, TokenID: 2, Policy: access.Policy{Mode: access.All}})
	result := g.ExecuteTo(other, &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
	if result.Error != nil || hits != 1 || len(a.requests) != 0 || len(d.requests) != 0 {
		t.Fatalf("static route disturbed: %+v", result)
	}
	channels := managedTestChannels()[1:]
	if err := g.Reload(channels); err != nil {
		t.Fatal(err)
	}
	if len(g.Models(other)) != 0 || len(g.Models(outputIdentity())) != 1 {
		t.Fatal("managed model grant leaked")
	}
	g.managed = nil
	if len(g.Models(outputIdentity())) != 0 {
		t.Fatal("disabled managed inference advertised model")
	}
}

func TestManagedRedirectPolicyKeepsStaticClientBehavior(t *testing.T) {
	for _, customRedirect := range []bool{false, true} {
		t.Run(fmt.Sprint(customRedirect), func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			jar, _ := cookiejar.New(nil)
			for _, origin := range []string{"https://api.openai.com", "http://static.test"} {
				r, _ := http.NewRequest(http.MethodGet, origin, nil)
				jar.SetCookies(r.URL, []*http.Cookie{{Name: "fixture", Value: "cookie"}})
			}
			staticHits, managedHits, redirects := 0, 0, 0
			client := &http.Client{Jar: jar, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "api.openai.com" {
					managedHits++
					if r.Header.Get("Cookie") != "" {
						t.Fatal("managed inference used static client cookies")
					}
					return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://untrusted.test/responses"}, "Set-Cookie": {"fixture=managed-response"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
				}
				if r.URL.Host != "static.test" {
					t.Fatal("managed inference followed cross-origin redirect")
				}
				staticHits++
				if r.Header.Get("Cookie") != "fixture=cookie" {
					t.Fatal("static route lost supplied client cookie jar")
				}
				if r.URL.Path != "/finished" {
					return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"http://static.test/finished"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(managedTestTerminal("completed")))}, nil
			})}
			if customRedirect {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { redirects++; return nil }
			}
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Channels: managedTestChannels(), Client: client,
				ManagedInference: &ManagedInferenceOptions{Enabled: true, Credentials: a, DirectoryCaller: d, ValidateModel: openaiauth.ValidateVisibleModel}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			other := access.WithPrincipal(context.Background(), access.Principal{UserID: 2, TokenID: 2, Policy: access.Policy{Mode: access.All}})
			result := g.ExecuteTo(other, &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
			if result.Error != nil || staticHits != 2 {
				t.Fatalf("static redirect disturbed: %+v hits=%d", result, staticHits)
			}
			result = g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
			if result.Error == nil || managedHits != 1 || staticHits != 2 || customRedirect && redirects != 1 {
				t.Fatalf("managed redirect policy: %+v managed=%d static=%d callbacks=%d", result, managedHits, staticHits, redirects)
			}
			r, _ := http.NewRequest(http.MethodGet, "https://api.openai.com", nil)
			cookies := jar.Cookies(r.URL)
			if len(cookies) != 1 || cookies[0].Value != "cookie" {
				t.Fatal("managed response changed supplied client cookie jar")
			}
		})
	}
}

func TestManagedDisabledChannelOrCredentialNeverFallsBackToStatic(t *testing.T) {
	for _, disabled := range []string{"channel", "credential"} {
		t.Run(disabled, func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			staticHits := 0
			g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "static.test" {
					t.Fatal("disabled managed grant dispatched")
				}
				staticHits++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(managedTestTerminal("completed")))}, nil
			})
			channels := managedTestChannels()
			if disabled == "channel" {
				channels[1].Status = model.StatusDisabled
			} else {
				channels[1].Credentials[0].Status = model.CredentialDisabled
			}
			if err := g.Publish(channels); err != nil {
				t.Fatal(err)
			}
			result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
			if result.Error == nil || result.StatusCode != 503 || staticHits != 0 || len(a.requests) != 0 || len(d.requests) != 0 {
				t.Fatalf("disabled grant fell back: %+v hits=%d", result, staticHits)
			}
			other := access.WithPrincipal(context.Background(), access.Principal{UserID: 2, TokenID: 2, Policy: access.Policy{Mode: access.All}})
			result = g.ExecuteTo(other, &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
			if result.Error != nil || staticHits != 1 {
				t.Fatalf("disabled grant disturbed independent static route: %+v hits=%d", result, staticHits)
			}
		})
	}
}

func TestManagedActiveReplacementIgnoresDisabledHistoricalGrants(t *testing.T) {
	a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
	hits := 0
	g := newManagedTestGateway(t, a, d, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.openai.com" {
			t.Fatal("replacement grant fell back to static")
		}
		hits++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(managedTestTerminal("completed")))}, nil
	})
	channels := managedTestChannels()
	for i := 1; i <= 2; i++ {
		old := channels[1]
		old.ID = uint(9 + i)
		old.Credentials = append([]model.Credential(nil), old.Credentials...)
		old.Credentials[0].ID = uint(90 + i)
		old.Credentials[0].ChannelID = old.ID
		old.Credentials[0].Status = model.CredentialDisabled
		if i == 1 {
			old.Status = model.StatusDisabled
		}
		channels = append(channels, old)
	}
	if err := g.Publish(channels); err != nil {
		t.Fatal(err)
	}
	result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
	if result.Error != nil || result.CredentialID != 90 || hits != 1 {
		t.Fatalf("disabled history blocked active replacement: %+v hits=%d", result, hits)
	}
	channels[1].Credentials[0].Status = model.CredentialDisabled
	if err := g.Publish(channels); err != nil {
		t.Fatal(err)
	}
	result = g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Responses, Body: []byte(managedTestBody)})
	if result.Error == nil || result.StatusCode != 503 || hits != 1 {
		t.Fatalf("all-disabled historical grants fell back: %+v hits=%d", result, hits)
	}
}
