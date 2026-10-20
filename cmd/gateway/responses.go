package main

import (
	"context"
	"errors"
	"log/slog"
	"minigateway/internal/compat/responseshttp"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/secrets"
	"net/http"
	"sync/atomic"
	"time"

	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/config"
	bg "minigateway/internal/foundation/contracts/background/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	authority "minigateway/internal/foundation/identity"
	bridge "minigateway/internal/foundation/runtime"
	"minigateway/internal/gateway"
	"minigateway/internal/generation"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	native "minigateway/internal/protocol/responses"
	"minigateway/internal/responses"
	"minigateway/internal/store"
)

type responsesRuntime struct {
	worker              *generation.BackgroundRunner
	handler             http.Handler
	history             history.Repository
	cancel              context.CancelFunc
	done                chan struct{}
	backgroundOccupancy atomic.Pointer[backgroundOccupancySample]
}

func startResponsesRuntime(cfg config.ResponsesConfig, st *store.Store, gw *gateway.Gateway) (*responsesRuntime, error) {
	if cfg.Background.Enabled && !cfg.Enabled {
		return nil, errors.New("background requires the Responses runtime")
	}
	if st == nil {
		if cfg.Enabled {
			return nil, errors.New("Responses store is required")
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.CleanupExpiredBackground(ctx); err != nil {
		return nil, errors.New("background retention initialization failed")
	}
	if gw != nil && gw.BillingMode() != "disabled" {
		guard, end := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := st.InitializeBackground(guard, "", gw.BillingMode())
		end()
		if err != nil {
			return nil, errors.New("retained background tasks require disabled billing")
		}
	}
	if !cfg.Enabled {
		background, stop := context.WithCancel(context.Background())
		r := &responsesRuntime{cancel: stop, done: make(chan struct{})}
		go r.retention(background, st, false)
		return r, nil
	}
	if gw == nil {
		return nil, errors.New("Responses gateway is required")
	}
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
	archive, err := generation.NewArchive(generation.ArchiveOptions{Store: persistence.Archive(st), Resolve: st.ResolvePrincipal, ActiveKey: keys.Active, Keys: keys.Keys,
		Retention: time.Duration(cfg.ReplaySeconds) * time.Second, MaxRecordBytes: cfg.MaxOutputBytes,
		Limits: model.GenerationArchiveLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords, InstanceBytes: cfg.InstanceBytes, OwnerBytes: cfg.OwnerBytes}})
	if err != nil {
		return nil, err
	}
	protector, err := secrets.NewRing(keys.Active, keys.Keys)
	if err != nil {
		return nil, err
	}
	historyRepository, err := generation.NewResponseHistory(generation.ResponseHistoryOptions{Store: persistence.History(st), Protector: protector, Retention: time.Duration(cfg.HistorySeconds) * time.Second,
		Limits: model.HistoryLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords, InstanceBytes: cfg.HistoryInstanceBytes, OwnerBytes: cfg.HistoryOwnerBytes}})
	if err != nil {
		return nil, err
	}
	history, err := responses.NewHistory(historyRepository)
	if err != nil {
		return nil, err
	}
	if err = st.RecoverResponseHistory(ctx); err != nil {
		return nil, errors.New("Responses history recovery failed")
	}
	var backgroundService *generation.BackgroundService
	var remote *gateway.BackgroundRemote
	var backgroundPort bg.Service
	var resources bg.Resources
	if cfg.Background.Enabled {
		channels := map[uint]string{}
		for _, id := range cfg.Background.ChannelIDs {
			channels[id] = native.BackgroundRegistrationID
		}
		remote, err = gateway.NewBackgroundRemote(gw, gateway.BackgroundRemoteOptions{Registrations: []bg.Registration{native.BackgroundRegistration()}, Channels: channels})
		if err != nil {
			return nil, err
		}
		b := cfg.Background
		backgroundService, err = generation.NewBackgroundService(ctx, generation.BackgroundOptions{Repository: st, Parser: native.BackgroundParser{}, History: historyRepository, Compiler: native.BackgroundHistoryCodec{}, Projector: native.BackgroundHistoryCodec{}, BillingMode: gw.BillingMode(), ActiveKey: keys.Active, Keys: keys.Keys, DigestKey: keys.Digest,
			Retention: time.Duration(b.RetentionSeconds) * time.Second, MetadataRetention: time.Duration(b.MetadataSeconds) * time.Second,
			Limits: bs.Limits{InstanceRecords: b.InstanceRecords, OwnerRecords: b.OwnerRecords, InstanceBytes: b.InstanceBytes, OwnerBytes: b.OwnerBytes, InstanceActive: b.InstanceActive, OwnerActive: b.OwnerActive}})
		if err != nil {
			return nil, err
		}
		backgroundPort, resources = backgroundService, backgroundService
	}
	service, err := responses.New(ctx, responses.Options{Background: backgroundPort, Resources: resources, History: history, Store: persistence.Receipts(st), Executor: bridge.NewExecutor(gw), Archive: generation.PublicArchive(archive), Identity: authority.New(st.ResolvePrincipal), DigestKey: keys.Digest,
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
	r := &responsesRuntime{handler: responseshttp.New(service, httpoutput.New), history: historyRepository, cancel: stop, done: make(chan struct{})}
	if backgroundService != nil {
		r.worker, err = generation.NewBackgroundRunner(background, generation.BackgroundRunnerOptions{Service: backgroundService, Remote: remote, Resolve: st.ResolvePrincipal})
		if err != nil {
			stop()
			return nil, err
		}
		r.handler = responseshttp.NewV2(service.V2(), httpoutput.New)
	}
	handler := r.handler
	r.handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithCancel(request.Context())
		stop := context.AfterFunc(background, cancel)
		defer func() { stop(); cancel() }()
		if background.Err() != nil {
			cancel()
		}
		handler.ServeHTTP(w, request.WithContext(ctx))
	})
	go r.retention(background, st, true)
	return r, nil
}

func (r *responsesRuntime) History() history.Repository {
	if r == nil {
		return nil
	}
	return r.history
}

func (r *responsesRuntime) retention(ctx context.Context, st *store.Store, foreground bool) {
	defer close(r.done)
	r.sampleBackgroundOccupancy(ctx, st)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			batch, end := context.WithTimeout(ctx, 2*time.Second)
			// Background privacy retention is independent of all product toggles
			// and of foreground maintenance failures. Expiry never reopens work.
			err := st.CleanupBackground(batch, 200)
			end()
			if err != nil && ctx.Err() == nil {
				slog.Warn("background retention cleanup did not finish")
			}
			r.sampleBackgroundOccupancy(ctx, st)
			if !foreground {
				continue
			}
			batch, end = context.WithTimeout(ctx, 2*time.Second)
			_, err = st.CleanupResponseRequests(batch, time.Now(), 200)
			if err == nil {
				_, err = st.CleanupResponseHistory(batch, 200)
			}
			if err == nil {
				_, err = st.CleanupGenerationRecords(batch, 200)
			}
			end()
			if err != nil && ctx.Err() == nil {
				slog.Warn("Responses retention cleanup did not finish")
			}
		}
	}
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
	_ = r.worker.Close(context.Background())
	<-r.done
}

// Stop ends admission/observation and interrupts owned RPCs before HTTP drain.
// Close still joins them before the shared gateway/store can be destroyed.
func (r *responsesRuntime) Stop() {
	if r != nil {
		r.cancel()
	}
}
