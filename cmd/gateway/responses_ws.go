package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"minigateway/internal/access"
	codec "minigateway/internal/compat/responsesws"
	presentation "minigateway/internal/compat/responseswshttp"
	bridge "minigateway/internal/compat/websocket"
	"minigateway/internal/config"
	execution "minigateway/internal/foundation/contracts/execution/v2"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	identity "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	application "minigateway/internal/responsesws"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
)

type responsesWSRuntime struct {
	service *gateway.NativeWebSocketService
	handler *presentation.Handler
	cancel  context.CancelFunc
}
type nativeResourceFactory func(context.Context, contract.Limits) (gateway.NativeWSResource, error)

func startResponsesWSRuntime(cfg config.ResponsesWSConfig, st *store.Store, gw *gateway.Gateway, limiter access.Limiter) (*responsesWSRuntime, error) {
	return startResponsesWSRuntimeWithFactory(cfg, st, gw, limiter, newOfficialNativeResource)
}

// Only assembly tests substitute a resource factory; configuration cannot
// redirect the production native endpoint or weaken its TLS/proxy policy.
func startResponsesWSRuntimeWithFactory(cfg config.ResponsesWSConfig, st *store.Store, gw *gateway.Gateway, limiter access.Limiter, factory nativeResourceFactory) (*responsesWSRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if st == nil || gw == nil || limiter == nil || factory == nil || gw.BillingMode() != "disabled" || !st.CredentialEncryptionReady(context.Background()) {
		return nil, errors.New("native websocket requires disabled billing and activated credential encryption")
	}
	root, cancel := context.WithCancel(context.Background())
	runtime := &responsesWSRuntime{cancel: cancel}
	limits := contract.Limits{MaxMessageBytes: 256 << 10, IdleTimeout: 60 * time.Second, RoundTimeout: 120 * time.Second, MaxLifetime: 55 * time.Minute}
	authority := identity.New(st.ResolvePrincipal)
	service, err := gateway.NewNativeWebSocketService(root, gw, gateway.NativeWSOptions{Authority: authority, Gate: access.NewRateGate(limiter), Models: cfg.Models, Limits: limits,
		Prepare: func(preparing context.Context) (gateway.NativeWSResource, error) {
			if err := preparing.Err(); err != nil {
				return nil, err
			}
			return factory(root, limits)
		},
		Parse: func(frame execution.Frame) (gateway.NativeWSCreate, error) {
			request, err := codec.Parse(frame)
			return gateway.NativeWSCreate{Model: request.Model, PreviousID: request.PreviousID, PromptText: request.PromptText, Initial: request.Initial}, err
		},
		Encode: codec.MapModel, Decoder: func(model string) execution.FrameDecoder { return codec.NewDecoder(model).Decode },
	})
	if err != nil {
		cancel()
		return nil, err
	}
	runtime.service = service
	fail := func(cause error) (*responsesWSRuntime, error) {
		cleanup, stop := context.WithTimeout(context.Background(), 130*time.Second)
		defer stop()
		if err := runtime.Close(cleanup); err != nil {
			return runtime, errors.New("native websocket startup cleanup is incomplete")
		}
		return nil, cause
	}
	channels, err := st.ChannelSnapshot()
	if err != nil {
		return fail(err)
	}
	preparing, stop := context.WithTimeout(root, 10*time.Second)
	err = service.Publish(preparing, channels)
	stop()
	if err != nil {
		return fail(err)
	}
	downstream, err := transport.New(root, transport.Options{Connections: 64, MaxMessageBytes: limits.MaxMessageBytes, HandshakeTimeout: 10 * time.Second, ReadTimeout: limits.RoundTimeout + limits.IdleTimeout, WriteTimeout: 10 * time.Second, MaxLifetime: limits.MaxLifetime, Origins: append([]string(nil), cfg.Origins...)})
	if err != nil {
		return fail(err)
	}
	handler, err := presentation.New(root, service, application.NewClientWithTerminal(limits.IdleTimeout, codec.IsTerminal), authority, downstream)
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 130*time.Second)
		_ = downstream.Close(cleanup)
		stop()
		return fail(err)
	}
	runtime.handler = handler
	return runtime, nil
}
func (r *responsesWSRuntime) Handler() http.Handler {
	if r == nil || r.handler == nil {
		return nil
	}
	return r.handler
}
func (r *responsesWSRuntime) Stop() {
	if r == nil {
		return
	}
	if r.handler != nil {
		r.handler.Stop()
	}
	if r.service != nil {
		r.service.Stop()
	}
}
func (r *responsesWSRuntime) Publish(channels []model.Channel) error {
	if r == nil || r.service == nil {
		return errors.New("native websocket publication is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.service.Publish(ctx, channels)
}
func (r *responsesWSRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.Stop()
	if r.service != nil {
		if err := r.service.Close(ctx); err != nil {
			return err
		}
	}
	if r.handler != nil {
		if err := r.handler.Close(ctx); err != nil {
			return err
		}
	}
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}
func joinResponsesWSRuntime(r *responsesWSRuntime) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	err := r.Close(ctx)
	cancel()
	if err == nil {
		return
	}
	slog.Error("native websocket drain incomplete; retaining shared dependencies until join")
	for {
		if r.Close(context.Background()) == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

type officialNativeResource struct{ manager *transport.Manager }

func newOfficialNativeResource(root context.Context, limits contract.Limits) (gateway.NativeWSResource, error) {
	manager, err := transport.New(root, transport.Options{Connections: 64, MaxMessageBytes: limits.MaxMessageBytes, HandshakeTimeout: 10 * time.Second, ReadTimeout: limits.RoundTimeout, WriteTimeout: 10 * time.Second, MaxLifetime: limits.MaxLifetime})
	if err != nil {
		return nil, err
	}
	return &officialNativeResource{manager: manager}, nil
}
func (r *officialNativeResource) Dial(ctx context.Context, key string) (execution.Connection, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, errors.New("native websocket connection identity unavailable")
	}
	connection, err := r.manager.Dial(ctx, "wss://api.openai.com/v1/responses", http.Header{"Authorization": {"Bearer " + key}})
	if err != nil {
		return nil, err
	}
	return bridge.Adapt(execution.ConnectionID(hex.EncodeToString(id)), connection), nil
}
func (r *officialNativeResource) Close(ctx context.Context) error { return r.manager.Close(ctx) }
