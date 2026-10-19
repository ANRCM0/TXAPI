// Package oauthloopback owns one bounded local HTTP authorization callback.
package oauthloopback

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

type receiver struct{ provider auth.Provider }

func New(provider auth.Provider) auth.Receiver { return receiver{provider: provider} }

type outcome struct {
	code auth.Code
	err  error
}

type session struct {
	provider     auth.Provider
	listener     net.Listener
	server       *http.Server
	host         string
	mu           sync.Mutex
	flow         auth.Flow
	bound        bool
	waited       bool
	terminal     bool
	closed       bool
	result       chan outcome
	boundAt      chan time.Time
	stop         chan struct{}
	serveDone    chan struct{}
	watchDone    chan struct{}
	shutdownOnce sync.Once
}

func (r receiver) Open(ctx context.Context) (auth.CallbackSession, error) {
	if ctx == nil || r.provider == nil {
		return nil, auth.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	s := &session{provider: r.provider, listener: listener, host: listener.Addr().String(), result: make(chan outcome, 1), boundAt: make(chan time.Time, 1), stop: make(chan struct{}), serveDone: make(chan struct{}), watchDone: make(chan struct{})}
	s.server = &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		err := s.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.complete(outcome{err: auth.ErrUnavailable})
		}
		close(s.serveDone)
	}()
	go s.watch(ctx)
	return s, nil
}

func (s *session) RedirectURI() string { return "http://" + s.host + "/auth/callback" }

func (s *session) Bind(flow auth.Flow) error {
	now := time.Now()
	if flow.RedirectURI != s.RedirectURI() || !auth.ValidWireText(flow.State.Value(), 128) || !flow.ExpiresAt.After(now) || flow.ExpiresAt.After(now.Add(10*time.Minute)) {
		return auth.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return auth.ErrUnavailable
	}
	if s.bound {
		return auth.ErrConflict
	}
	s.flow = flow
	s.bound = true
	s.boundAt <- flow.ExpiresAt
	return nil
}

func (s *session) complete(value outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return
	}
	s.terminal = true
	s.result <- value
}

func (s *session) watch(ctx context.Context) {
	defer close(s.watchDone)
	timer := time.NewTimer(10*time.Minute + 5*time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case expires := <-s.boundAt:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Until(expires))
		case <-ctx.Done():
			s.complete(outcome{err: ctx.Err()})
			s.shutdown()
			return
		case <-timer.C:
			s.complete(outcome{err: context.DeadlineExceeded})
			s.shutdown()
			return
		case <-s.serveDone:
			s.complete(outcome{err: auth.ErrUnavailable})
			s.shutdown()
			return
		}
	}
}

func (s *session) shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stop)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.server.Shutdown(ctx); err != nil {
			_ = s.server.Close()
		}
		cancel()
		_ = s.listener.Close()
		<-s.serveDone
	})
}

func (s *session) Close() error { s.shutdown(); <-s.watchDone; return nil }

func (s *session) Wait(ctx context.Context) (auth.Code, error) {
	if ctx == nil {
		return auth.Code{}, auth.ErrInvalid
	}
	s.mu.Lock()
	if s.waited {
		s.mu.Unlock()
		return auth.Code{}, auth.ErrConflict
	}
	if !s.bound {
		s.mu.Unlock()
		return auth.Code{}, auth.ErrInvalid
	}
	s.waited = true
	s.mu.Unlock()
	defer s.Close()
	select {
	case value := <-s.result:
		return value.code, value.err
	case <-ctx.Done():
		return auth.Code{}, ctx.Err()
	case <-s.stop:
		select {
		case value := <-s.result:
			return value.code, value.err
		default:
			return auth.Code{}, auth.ErrUnavailable
		}
	}
}

func staticResponse(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if status == http.StatusOK {
		_, _ = io.WriteString(w, "Authorization received. You may close this page.\n")
	} else {
		_, _ = io.WriteString(w, "Authorization callback unavailable or rejected.\n")
	}
}

func (s *session) handle(w http.ResponseWriter, request *http.Request) {
	remoteHost, _, err := net.SplitHostPort(request.RemoteAddr)
	ip := net.ParseIP(remoteHost)
	if err != nil || ip == nil || !ip.IsLoopback() || request.Method != http.MethodGet || request.Host != s.host || request.URL.IsAbs() || request.URL.Host != "" || request.URL.Opaque != "" || request.URL.Path != "/auth/callback" || request.URL.RawPath != "" || request.URL.Fragment != "" || request.URL.User != nil || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || (request.Body != nil && request.Body != http.NoBody) || len(request.URL.RawQuery) > 16*1024 {
		staticResponse(w, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.closed || (s.bound && !time.Now().Before(s.flow.ExpiresAt)) {
		s.mu.Unlock()
		staticResponse(w, http.StatusGone)
		return
	}
	if !s.bound {
		s.mu.Unlock()
		staticResponse(w, http.StatusBadRequest)
		return
	}
	if s.terminal {
		s.mu.Unlock()
		staticResponse(w, http.StatusConflict)
		return
	}
	code, callbackErr := s.provider.Callback(s.flow, request.URL.RawQuery)
	if callbackErr != nil && !errors.Is(callbackErr, auth.ErrRejected) {
		s.mu.Unlock()
		staticResponse(w, http.StatusBadRequest)
		return
	}
	s.terminal = true
	s.result <- outcome{code: code, err: callbackErr}
	s.mu.Unlock()
	staticResponse(w, http.StatusOK)
}

var _ auth.CallbackSession = (*session)(nil)
