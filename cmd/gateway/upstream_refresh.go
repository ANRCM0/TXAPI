package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"minigateway/internal/compat/oauthloopback"
	"minigateway/internal/config"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/foundation/upstreamhttp"
	"minigateway/internal/protocol/openaiauth"
	"minigateway/internal/store"
	"minigateway/internal/upstreamidentity"
)

// The runtime retains its caller until its worker joins, including after a
// timed-out Close. Production supplies no alternate HTTP client.
type upstreamRefreshRuntime struct {
	worker *upstreamidentity.RefreshWorker
	caller auth.HTTPCaller
	cancel context.CancelFunc
	closed sync.Once
}

func startUpstreamRefreshRuntime(parent context.Context, cfg config.UpstreamAccountsConfig, st *store.Store, publish func(context.Context) error, client *http.Client) (*upstreamRefreshRuntime, error) {
	if !cfg.Refresh.Enabled {
		return nil, nil
	}
	if parent == nil || st == nil || publish == nil {
		return nil, auth.ErrInvalid
	}
	check, cancel := context.WithTimeout(parent, 5*time.Second)
	ready := st.CredentialEncryptionReady(check)
	cancel()
	if !ready {
		return nil, auth.ErrEncryptionRequired
	}
	provider := openaiauth.New()
	caller := upstreamhttp.NewCaller(client)
	manager, err := upstreamidentity.New(st.UpstreamAccounts(), provider, caller, oauthloopback.New(provider))
	if err != nil {
		caller.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(parent)
	worker, err := upstreamidentity.NewRefreshWorker(ctx, upstreamidentity.RefreshWorkerOptions{
		Manager: manager, Publish: publish,
		Interval:     time.Duration(cfg.Refresh.IntervalSeconds) * time.Second,
		BeforeExpiry: time.Duration(cfg.Refresh.BeforeExpirySeconds) * time.Second,
		MaxAttempts:  cfg.Refresh.MaxAttemptsPerCycle,
	})
	if err != nil {
		stop()
		caller.Close()
		return nil, err
	}
	return &upstreamRefreshRuntime{worker: worker, caller: caller, cancel: stop}, nil
}

func (r *upstreamRefreshRuntime) Stop() {
	if r != nil {
		r.cancel()
	}
}

func (r *upstreamRefreshRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.Stop()
	if err := r.worker.Close(ctx); err != nil {
		return err
	}
	r.closed.Do(r.caller.Close)
	return nil
}

func joinUpstreamRefreshRuntime(r *upstreamRefreshRuntime) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := r.Close(ctx)
	cancel()
	if err != nil {
		slog.Error("upstream refresh drain incomplete; retaining shared dependencies until join")
		_ = r.Close(context.Background())
	}
}

func refreshPublication(ctx context.Context, apply func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !apply() {
		return errors.New("upstream account publication is pending")
	}
	return ctx.Err()
}
