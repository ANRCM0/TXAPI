package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	bridge "minigateway/internal/foundation/runtime"
	"minigateway/internal/gateway"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

// The application root owns startup/close. No producer starts inside a store,
// gateway option, package initializer, HTTP handshake or request goroutine.
type generationRuntime struct {
	acceptor *generation.IntentAcceptor
	archive  *generation.Archive
	runner   *generation.DurableRunner
	cancel   context.CancelFunc
	done     chan struct{}
}

func startGenerationRuntime(cfg config.GenerationConfig, st *store.Store, engine *billing.Engine, gw *gateway.Gateway) (*generationRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	startup, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startupCancel()
	if !st.CredentialEncryptionReady(startup) {
		return nil, fmt.Errorf("durable generation requires activated upstream credential encryption")
	}
	if engine == nil || gw == nil {
		return nil, fmt.Errorf("durable generation requires initialized execution dependencies")
	}
	if !engine.StrictReady() {
		return nil, fmt.Errorf("durable generation requires the initialized strict ledger")
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
	requirements, err := st.GenerationKeyRequirements(startup)
	if err != nil {
		return nil, fmt.Errorf("generation key requirements cannot be read")
	}
	version, err := st.GetSetting("generation.intent_digest_version")
	if err != nil {
		return nil, fmt.Errorf("generation digest configuration cannot be read")
	}
	if err := keys.ValidateRequirements(requirements, version); err != nil {
		return nil, fmt.Errorf("retained generation content requires missing or incompatible keys")
	}
	acceptor, err := generation.NewIntentAcceptor(generation.IntentOptions{Admission: engine, Resolve: st.ResolvePrincipal,
		ActiveKey: keys.Active, Keys: keys.Keys, DigestKey: keys.Digest,
		QueueTTL: time.Duration(cfg.DeadlineSeconds) * time.Second, Retention: time.Duration(cfg.RetentionSeconds) * time.Second,
		Limits: model.GenerationIntentLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords, InstanceBytes: cfg.InstanceBytes, OwnerBytes: cfg.OwnerBytes}})
	if err != nil {
		return nil, err
	}
	archive, err := generation.NewArchive(generation.ArchiveOptions{Store: st, Resolve: st.ResolvePrincipal, ActiveKey: keys.Active, Keys: keys.Keys,
		Retention: time.Duration(cfg.RetentionSeconds) * time.Second, MaxRecordBytes: cfg.MaxOutputBytes,
		Limits: model.GenerationArchiveLimits{InstanceRecords: cfg.InstanceRecords, OwnerRecords: cfg.OwnerRecords, InstanceBytes: cfg.InstanceBytes, OwnerBytes: cfg.OwnerBytes}})
	if err != nil {
		return nil, err
	}
	for {
		n, err := st.InterruptGenerationRecords(startup, 200)
		if err != nil {
			return nil, fmt.Errorf("generation output recovery failed")
		}
		if n == 0 {
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner, err := generation.NewDurableRunner(ctx, generation.DurableOptions{Acceptor: acceptor, Leases: engine, Archive: archive, Workers: cfg.Workers,
		PollInterval: time.Duration(cfg.PollMilliseconds) * time.Millisecond, LeaseDuration: time.Duration(cfg.LeaseSeconds) * time.Second, Execute: bridge.NewExecutor(gw).ExecuteClaimed})
	if err != nil {
		cancel()
		return nil, err
	}
	r := &generationRuntime{acceptor: acceptor, archive: archive, runner: runner, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				batch, batchCancel := context.WithTimeout(ctx, 2*time.Second)
				_, recoveryErr := engine.RecoverGenerationIntents(batch, time.Now(), 200)
				if recoveryErr == nil {
					_, recoveryErr = engine.CleanupGenerationIntents(batch, 200)
				}
				if recoveryErr == nil {
					_, recoveryErr = st.CleanupGenerationRecords(batch, 200)
				}
				batchCancel()
				if recoveryErr != nil && ctx.Err() == nil {
					slog.Warn("generation recovery or retention cleanup did not finish")
				}
			}
		}
	}()
	return r, nil
}

func (r *generationRuntime) Stop() {
	if r != nil {
		r.cancel()
	}
}

func (r *generationRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.Stop()
	if err := r.runner.Close(ctx); err != nil {
		return err
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
