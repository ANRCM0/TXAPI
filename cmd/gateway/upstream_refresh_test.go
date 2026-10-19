package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/oauthloopback"
	"minigateway/internal/config"
	"minigateway/internal/core"
	cfgv1 "minigateway/internal/foundation/contracts/configuration/v1"
	cfgv2 "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/upstreamhttp"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
	"minigateway/internal/store"
	"minigateway/internal/upstreamidentity"
)

const refreshFixtureID = "ua_00000000000000000000000000000091"

func seedRefreshAccount(t *testing.T, st *store.Store) auth.Account {
	t.Helper()
	account, err := st.UpstreamAccounts().SaveAuthorization(context.Background(), auth.AuthorizationWrite{
		ID: refreshFixtureID, Provider: "openai", Name: "synthetic refresh", ClientID: "synthetic-issued-client",
		Identity: auth.Identity{Issuer: "https://auth.openai.com", Subject: "synthetic-subject"},
		Tokens:   auth.Tokens{AccessToken: auth.NewSecret("synthetic-old-access"), RefreshToken: auth.NewSecret("synthetic-old-refresh"), IDToken: auth.NewSecret("synthetic-id-token"), Nonce: auth.NewSecret("synthetic-nonce"), ExpiresAt: time.Now().Add(2 * time.Minute), ScopesKnown: true, Scopes: []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only scheduling metadata changes; encrypted token expiry remains intact.
	if err := st.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", account.ID).Update("next_refresh_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func refreshReply() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-new-access","refresh_token":"synthetic-new-refresh","token_type":"Bearer","expires_in":3600}`))}
}

func checkRefreshRequest(r *http.Request) error {
	if r.Method != http.MethodPost || r.URL.String() != "https://auth.openai.com/api/accounts/oauth/token" {
		return errors.New("wrong refresh target")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	values, err := url.ParseQuery(string(body))
	if err != nil || values.Get("grant_type") != "refresh_token" || values.Get("resource") != "https://api.openai.com/v1" || values.Get("client_id") != "synthetic-issued-client" || values.Get("refresh_token") != "synthetic-old-refresh" {
		return errors.New("wrong refresh form")
	}
	return nil
}

func refreshTestConfig() config.UpstreamAccountsConfig {
	var cfg config.UpstreamAccountsConfig
	cfg.Refresh.Enabled = true
	cfg.Refresh.IntervalSeconds = 30
	cfg.Refresh.BeforeExpirySeconds = 600
	cfg.Refresh.MaxAttemptsPerCycle = 4
	return cfg
}

func assertRestartedRefreshCredentialReadable(t *testing.T, st *store.Store, account auth.Account) {
	t.Helper()
	owner, err := st.BootstrapAdmin("crash-admin", "synthetic-crash-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(owner.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := st.ManagedChannels().CreateManagedChannel(context.Background(), &cfgv2.ManagedChannelInput{
		Channel: cfgv1.ChannelInput{Name: "crash-managed", Type: "openai_siwc", BaseURL: "https://api.openai.com/v1", Models: []string{"synthetic-model"}},
		Binding: cfgv2.ManagedBindingInput{AccountID: account.ID, OwnerID: owner.ID, TokenID: key.ID, Model: "synthetic-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := st.InferenceCredentials().AcquireAccess(context.Background(), authv2.AccessRequest{CredentialID: bound.Credential.CredentialID, ExpectedBindingRevision: bound.Credential.Revision, AccountID: account.ID, ExpectedRevision: account.Revision, ExpectedCredentialRevision: account.CredentialRevision, OwnerID: owner.ID, TokenID: key.ID, Model: "synthetic-model"})
	if err != nil || credential.AccessToken.Value() != "synthetic-new-access" {
		t.Fatal("committed rotated token not readable after process restart", err)
	}
}

func TestUpstreamRefreshRuntimeUsesRealEncryptedManagerAndPublishesNewGeneration(t *testing.T) {
	if runtime, err := startUpstreamRefreshRuntime(context.Background(), config.UpstreamAccountsConfig{}, nil, nil, nil); err != nil || runtime != nil {
		t.Fatal("disabled refresh touched dependencies", err)
	}
	plain, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plain.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if runtime, err := startUpstreamRefreshRuntime(context.Background(), refreshTestConfig(), plain, func(context.Context) error { return nil }, nil); err == nil || runtime != nil {
		t.Fatal("unencrypted refresh enabled")
	}
	db := config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "refresh.db"), CredentialKeysFile: runtimeKeyFile(t, "refresh", 67, 71)}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	before := seedRefreshAccount(t, st)
	owner, err := st.BootstrapAdmin("refresh-admin", "synthetic-refresh-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(owner.ID, &store.TokenInput{Name: "refresh-inference", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ManagedChannels().CreateManagedChannel(context.Background(), &cfgv2.ManagedChannelInput{
		Channel: cfgv1.ChannelInput{Name: "refresh-managed", Type: "openai_siwc", BaseURL: "https://api.openai.com/v1", Models: []string{"synthetic-model"}},
		Binding: cfgv2.ManagedBindingInput{AccountID: before.ID, OwnerID: owner.ID, TokenID: key.ID, Model: "synthetic-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var inference atomic.Int64
	client := &http.Client{Transport: generationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == "https://api.openai.com/v1/models" || r.URL.String() == "https://api.openai.com/v1/responses" {
			if r.Header.Get("Authorization") != "Bearer synthetic-new-access" {
				return nil, errors.New("stale inference credential")
			}
			if r.URL.Path == "/v1/models" {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-model","visibility":"list"}]}`))}, nil
			}
			inference.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"))}, nil
		}
		if err := checkRefreshRequest(r); err != nil {
			return nil, err
		}
		calls.Add(1)
		return refreshReply(), nil
	})}
	directory := upstreamhttp.NewCaller(client)
	defer directory.Close()
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(), BillingMode: billing.ModeDisabled, Client: client,
		ManagedInference: &gateway.ManagedInferenceOptions{Enabled: true, Credentials: st.InferenceCredentials(), DirectoryCaller: directory, ValidateModel: openaiauth.ValidateVisibleModel}})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	published := make(chan auth.Account, 8)
	runtime, err := startUpstreamRefreshRuntime(context.Background(), refreshTestConfig(), st, func(ctx context.Context) error {
		channels, err := st.ChannelSnapshot()
		if err != nil {
			return err
		}
		if err := gw.Publish(channels); err != nil {
			return err
		}
		account, err := st.UpstreamAccounts().Read(ctx, before.ID)
		if err == nil {
			select {
			case published <- account:
			default:
			}
		}
		return err
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case account := <-published:
			if account.CredentialRevision <= before.CredentialRevision {
				continue
			}
			if account.State != auth.Active || calls.Load() != 1 {
				t.Fatal("refresh did not publish one active generation")
			}
			trusted, _, ok := identity.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
			if !ok {
				t.Fatal("inference key authentication failed")
			}
			output := httptest.NewRecorder()
			result := gw.ExecuteTo(trusted, core.NewHTTPOutput(output), gateway.Execution{Operation: protocol.Responses, Body: []byte(`{"model":"synthetic-model","input":[{"role":"user","content":"hello"}],"store":false,"stream":true}`)})
			if result.Error != nil || result.StatusCode != 200 || !result.ProtocolComplete || inference.Load() != 1 {
				t.Fatalf("published generation unusable for P1 consumer: status=%d calls=%d", result.StatusCode, inference.Load())
			}
			if err := runtime.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			stored, err := reopened.UpstreamAccounts().Read(context.Background(), account.ID)
			if err != nil || stored.Revision != account.Revision || stored.CredentialRevision != account.CredentialRevision {
				t.Fatal("published generation did not survive restart", err)
			}
			claim, err := reopened.UpstreamAccounts().ClaimRefresh(context.Background(), stored.ID, stored.Revision)
			if !errors.Is(err, auth.ErrBackoff) {
				t.Fatal("refresh cooldown missing", claim, err)
			}
			return
		case <-timer.C:
			t.Fatal("refreshed generation was never published")
		}
	}
}

type refreshClosingTransport struct {
	entered chan struct{}
	release chan struct{}
	closed  atomic.Int64
}

func (t *refreshClosingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := checkRefreshRequest(r); err != nil {
		return nil, err
	}
	close(t.entered)
	<-t.release
	return refreshReply(), nil
}
func (t *refreshClosingTransport) CloseIdleConnections() { t.closed.Add(1) }

func TestUpstreamRefreshRuntimeTimedOutCloseRetainsCallerUntilJoined(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "shutdown.db"), CredentialKeysFile: runtimeKeyFile(t, "shutdown", 83, 89)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedRefreshAccount(t, st)
	transport := &refreshClosingTransport{entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := startUpstreamRefreshRuntime(context.Background(), refreshTestConfig(), st, func(context.Context) error { return nil }, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		_ = runtime.Close(context.Background())
		t.Fatal("refresh never reached transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = runtime.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || transport.closed.Load() != 0 {
		close(transport.release)
		_ = runtime.Close(context.Background())
		t.Fatal("caller closed before worker joined", err)
	}
	close(transport.release)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil || transport.closed.Load() != 1 {
		t.Fatal("joined runtime failed to close owned caller exactly once", err)
	}
	if err := runtime.Close(ctx); err != nil || transport.closed.Load() != 1 {
		t.Fatal("repeated close changed caller ownership", err)
	}
}

type checkpointRefreshRepository struct {
	auth.Repository
	stage, marker string
}

func (r checkpointRefreshRepository) checkpoint(stage string) {
	if r.stage != stage {
		return
	}
	if err := os.WriteFile(r.marker, []byte(stage), 0600); err != nil {
		panic(err)
	}
	select {}
}
func (r checkpointRefreshRepository) ClaimRefresh(ctx context.Context, id string, revision int64) (auth.Claim, error) {
	claim, err := r.Repository.ClaimRefresh(ctx, id, revision)
	if err == nil {
		r.checkpoint("claimed")
	}
	return claim, err
}
func (r checkpointRefreshRepository) StartRefresh(ctx context.Context, claim auth.Claim) (auth.Credential, error) {
	credential, err := r.Repository.StartRefresh(ctx, claim)
	if err == nil {
		r.checkpoint("started")
	}
	return credential, err
}
func (r checkpointRefreshRepository) CompleteRefresh(ctx context.Context, claim auth.Claim, tokens auth.Tokens) (auth.Account, error) {
	r.checkpoint("returned")
	account, err := r.Repository.CompleteRefresh(ctx, claim, tokens)
	if err == nil {
		r.checkpoint("completed")
	}
	return account, err
}

func TestUpstreamRefreshCrashChild(t *testing.T) {
	stage := os.Getenv("TXAPI_REFRESH_CRASH_STAGE")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	st, err := store.Open(config.DatabaseConfig{Path: os.Getenv("TXAPI_REFRESH_CRASH_DB"), CredentialKeysFile: os.Getenv("TXAPI_REFRESH_CRASH_KEYS")})
	if err != nil {
		t.Fatal(err)
	}
	repository := checkpointRefreshRepository{Repository: st.UpstreamAccounts(), stage: stage, marker: os.Getenv("TXAPI_REFRESH_CRASH_MARKER")}
	provider := openaiauth.New()
	caller := upstreamhttp.NewCaller(&http.Client{Transport: generationTransport(func(r *http.Request) (*http.Response, error) {
		if err := checkRefreshRequest(r); err != nil {
			return nil, err
		}
		if err := os.WriteFile(os.Getenv("TXAPI_REFRESH_CRASH_SENT"), []byte("one"), 0600); err != nil {
			return nil, err
		}
		return refreshReply(), nil
	})})
	manager, err := upstreamidentity.New(repository, provider, caller, oauthloopback.New(provider))
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstreamidentity.NewRefreshWorker(context.Background(), upstreamidentity.RefreshWorkerOptions{Manager: manager, Publish: func(context.Context) error { return nil }, Interval: 30 * time.Second, BeforeExpiry: 10 * time.Minute, MaxAttempts: 4})
	if err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestUpstreamRefreshProcessKillRecoveryAtDurableBoundaries(t *testing.T) {
	for _, stage := range []string{"claimed", "started", "returned", "completed"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			db := config.DatabaseConfig{Path: filepath.Join(dir, "crash.db"), CredentialKeysFile: runtimeKeyFile(t, "crash", 73, 79)}
			st, err := store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			before := seedRefreshAccount(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			marker, sent := filepath.Join(dir, "checkpoint"), filepath.Join(dir, "sent")
			command := exec.Command(os.Args[0], "-test.run=^TestUpstreamRefreshCrashChild$", "-test.count=1", "-test.timeout=30s")
			command.Env = append(os.Environ(), "TXAPI_REFRESH_CRASH_STAGE="+stage, "TXAPI_REFRESH_CRASH_DB="+db.Path, "TXAPI_REFRESH_CRASH_KEYS="+db.CredentialKeysFile, "TXAPI_REFRESH_CRASH_MARKER="+marker, "TXAPI_REFRESH_CRASH_SENT="+sent)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = command.Process.Kill()
					_ = command.Wait()
					waited = true
					t.Fatalf("child checkpoint timeout: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("child exited gracefully")
			}
			waited = true
			_, sentErr := os.Stat(sent)
			if (stage == "returned" || stage == "completed") != (sentErr == nil) {
				t.Fatal("checkpoint does not match actual token HTTP dispatch")
			}
			st, err = store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			// Advance only lease metadata after a real Kill+Wait, avoiding a 90s
			// wall-clock wait. Never alter token expiry or its encrypted envelope.
			if stage != "completed" {
				if err := st.DB().Model(&model.ManagedUpstreamAccount{}).Where("id = ?", before.ID).Update("claim_until", time.Now().UTC().Add(-time.Second)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.UpstreamAccounts().Recover(context.Background(), 1000); err != nil {
				t.Fatal(err)
			}
			account, err := st.UpstreamAccounts().Read(context.Background(), before.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "claimed":
				if account.State != auth.Active || account.LastFailure != auth.NotSentFailure || account.CredentialRevision != before.CredentialRevision {
					t.Fatal("unsent claim did not recover safely", account)
				}
			case "started", "returned":
				if account.State != auth.Uncertain || account.LastFailure != auth.UnknownFailure || account.CredentialRevision != before.CredentialRevision {
					t.Fatal("possibly rotated credential became retryable", account)
				}
			case "completed":
				if account.State != auth.Active || account.CredentialRevision != before.CredentialRevision+1 {
					t.Fatal("completed credential generation lost", account)
				}
				assertRestartedRefreshCredentialReadable(t, st, account)
			}
			var sends atomic.Int64
			scanned := make(chan struct{}, 1)
			runtime, err := startUpstreamRefreshRuntime(context.Background(), refreshTestConfig(), st, func(context.Context) error {
				select {
				case scanned <- struct{}{}:
				default:
				}
				return nil
			}, &http.Client{Transport: generationTransport(func(*http.Request) (*http.Response, error) { sends.Add(1); return refreshReply(), nil })})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-scanned:
			case <-time.After(5 * time.Second):
				_ = runtime.Close(context.Background())
				t.Fatal("restart did not scan recovered metadata")
			}
			// Await a completed metadata scan before joining the worker; uncertain,
			// cooldown and fresh completed generations cannot refresh on restart.
			if err := runtime.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if sends.Load() != 0 {
				t.Fatal("restart replayed a consumed or deferred refresh")
			}
		})
	}
}
