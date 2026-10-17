package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	"minigateway/internal/responses"
	"minigateway/internal/store"
)

type responsesRuntime struct {
	handler http.Handler
	cancel  context.CancelFunc
	done    chan struct{}
}

func startResponsesRuntime(cfg config.ResponsesConfig, st *store.Store, gw *gateway.Gateway) (*responsesRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !st.CredentialEncryptionReady(ctx) {
		return nil, errors.New("Responses requires activated upstream credential encryption")
	}
	keys, err := generation.ReadContentKeyFile(cfg.KeysFile)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, key := range keys.Keys {
			clear(key)
		}
		clear(keys.Digest)
	}()
	requirements, err := st.GenerationKeyRequirements(ctx)
	if err != nil {
		return nil, errors.New("Responses content key requirements are unavailable")
	}
	version, err := st.GetSetting("generation.intent_digest_version")
	if err != nil || keys.ValidateRequirements(requirements, version) != nil {
		return nil, errors.New("retained content requires missing or incompatible keys")
	}
	archive, err := generation.NewArchive(generation.ArchiveOptions{Store: st, Resolve: st.ResolvePrincipal, ActiveKey: keys.Active, Keys: keys.Keys,
		Retention: time.Duration(cfg.ReplaySeconds) * time.Second, MaxRecordBytes: cfg.MaxOutputBytes,
		Limits: model.GenerationArchiveLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords, InstanceBytes: cfg.InstanceBytes, OwnerBytes: cfg.OwnerBytes}})
	if err != nil {
		return nil, err
	}
	service, err := responses.New(ctx, responses.Options{Store: st, Executor: gw, Archive: archive, Resolve: st.ResolvePrincipal, DigestKey: keys.Digest,
		Retention: time.Duration(cfg.RetentionSeconds) * time.Second, Limits: model.ResponseRequestLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords},
		ActiveRequests: cfg.ActiveRequests, OwnerRequests: cfg.OwnerRequests})
	if err != nil {
		return nil, err
	}
	// This runs before any HTTP producer or durable worker starts.
	for {
		n, err := st.RecoverResponseRequests(ctx, 200)
		if err != nil {
			return nil, errors.New("Responses receipt recovery failed")
		}
		if n == 0 {
			break
		}
	}
	for {
		n, err := st.InterruptGenerationRecords(ctx, 200)
		if err != nil {
			return nil, errors.New("Responses output recovery failed")
		}
		if n == 0 {
			break
		}
	}
	background, stop := context.WithCancel(context.Background())
	r := &responsesRuntime{handler: service, cancel: stop, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-background.Done():
				return
			case <-ticker.C:
				batch, end := context.WithTimeout(background, 2*time.Second)
				_, err := st.CleanupResponseRequests(batch, time.Now(), 200)
				if err == nil {
					_, err = st.CleanupGenerationRecords(batch, 200)
				}
				end()
				if err != nil && background.Err() == nil {
					slog.Warn("Responses retention cleanup did not finish")
				}
			}
		}
	}()
	return r, nil
}

func (r *responsesRuntime) Handler() http.Handler {
	if r == nil {
		return nil
	}
	return r.handler
}

func (r *responsesRuntime) Close() {
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}
