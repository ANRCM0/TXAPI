package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/store"
)

func TestManagedFunctionNextRoundRechecksPersistentAuthority(t *testing.T) {
	for _, transition := range []string{"disable", "reauthorize", "refresh_claimed", "refresh_started", "uncertain", "rotated", "key_revoked", "model_revoked"} {
		t.Run(transition, func(t *testing.T) {
			f := newManagedFunctionSDKFixture(t)
			post := func(body string) (int, string) {
				t.Helper()
				req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.front.URL+"/v1/responses", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+f.key.Key)
				req.Header.Set("Content-Type", "application/json")
				response, err := f.front.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, string(raw)
			}
			tool := `{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false},"strict":true}`
			first := `{"model":"synthetic-model","stream":true,"store":false,"input":[{"role":"user","content":"weather?"}],"tools":[` + tool + `],"tool_choice":"auto"}`
			if code, raw := post(first); code != 200 || !strings.Contains(raw, "response.completed") || !strings.Contains(raw, "call_managed_weather") {
				t.Fatalf("first function round: %d %s", code, raw)
			}
			beforePosts, beforeCatalogs := f.attempts.Load(), f.catalogs.Load()
			ctx := context.Background()
			repo := f.st.UpstreamAccounts()
			var err error
			switch transition {
			case "disable":
				_, err = repo.Disable(ctx, f.account.ID, f.account.Revision)
			case "reauthorize":
				_, err = repo.BeginAuthorization(ctx, f.account.ID, f.account.Revision)
			case "key_revoked":
				err = f.st.DeleteToken(f.key.ID)
			case "model_revoked":
				_, err = f.st.UpdateToken(f.key.ID, &store.TokenInput{Name: "restricted", ModelMode: access.Allowlist, Models: []string{"other-model"}})
			default:
				// Observe the real repository cooldown; no mutation of protected rows
				// or substitute clock is needed to create a refresh claim.
				if wait := time.Until(f.account.NextRefreshAt); wait > 0 {
					select {
					case <-time.After(wait + time.Millisecond):
					case <-f.ctx.Done():
						t.Fatal("fixture expired before refresh")
					}
				}
				var claim auth.Claim
				claim, err = repo.ClaimRefresh(ctx, f.account.ID, f.account.Revision)
				if err == nil && transition != "refresh_claimed" {
					_, err = repo.StartRefresh(ctx, claim)
				}
				if err == nil && transition == "uncertain" {
					_, err = repo.FinishRefresh(ctx, claim, auth.UnknownFailure)
				}
				if err == nil && transition == "rotated" {
					_, err = repo.CompleteRefresh(ctx, claim, auth.Tokens{AccessToken: auth.NewSecret("rotated-fixture-access"), RefreshToken: auth.NewSecret("rotated-fixture-refresh"), IDToken: auth.NewSecret("rotated-fixture-id"), Nonce: auth.NewSecret("rotated-fixture-nonce"), ExpiresAt: time.Now().Add(time.Hour), ScopesKnown: f.account.ScopesKnown, Scopes: f.account.Scopes})
				}
			}
			if err != nil {
				t.Fatal("authority transition", transition, err)
			}
			next := fmt.Sprintf(`{"model":"synthetic-model","stream":true,"store":false,"input":[{"role":"user","content":"weather?"},{"type":"function_call","id":"fc_managed_weather","call_id":"call_managed_weather","name":"weather","arguments":"{\"city\":\"\u4e0a\u6d77\"}","status":"completed"},{"type":"function_call_output","call_id":"call_managed_weather","output":"24 C"}],"tools":[%s],"tool_choice":"none"}`, tool)
			code, raw := post(next)
			if code < 400 || strings.Contains(raw, "response.completed") || f.attempts.Load() != beforePosts || f.catalogs.Load() != beforeCatalogs {
				t.Fatalf("changed authority sent a tool result: %d %s posts=%d/%d catalogs=%d/%d", code, raw, beforePosts, f.attempts.Load(), beforeCatalogs, f.catalogs.Load())
			}
		})
	}
}
