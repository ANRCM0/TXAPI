// Package responses applies durable foreground idempotency around the shared
// gateway execution port. It owns neither attempts nor billing settlement.
package responses

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/jsonutil"
)

const MaxRequestBytes = 1 << 20

type ReceiptStore interface {
	InitializeResponseRequests(context.Context, string) error
	BeginResponseRequest(context.Context, model.ResponseRequest, model.ResponseRequestLimits) (model.ResponseRequest, bool, error)
	FinishResponseRequest(context.Context, model.ResponseRequest, model.ResponseRequestCompletion) error
}

type Executor interface {
	ExecuteTo(context.Context, core.Output, gateway.Execution) gateway.RequestResult
}

type Options struct {
	Store                         ReceiptStore
	Executor                      Executor
	Archive                       *generation.Archive
	Resolve                       func(context.Context, access.Binding) (access.Principal, error)
	DigestKey                     []byte
	Retention                     time.Duration
	Limits                        model.ResponseRequestLimits
	ActiveRequests, OwnerRequests int
}

type Service struct {
	store                       ReceiptStore
	executor                    Executor
	archive                     *generation.Archive
	resolve                     func(context.Context, access.Binding) (access.Principal, error)
	digest                      []byte
	version                     string
	retention                   time.Duration
	limits                      model.ResponseRequestLimits
	mu                          sync.Mutex
	active, maxActive, maxOwner int
	owners                      map[uint]int
}

func missing(v any) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil())
}

func New(ctx context.Context, opts Options) (*Service, error) {
	if missing(opts.Store) || missing(opts.Executor) || opts.Archive == nil || opts.Resolve == nil || len(opts.DigestKey) != 32 ||
		opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords <= opts.Limits.OwnerRecords ||
		opts.ActiveRequests < 2 || opts.ActiveRequests > 128 || opts.OwnerRequests < 1 || opts.OwnerRequests >= opts.ActiveRequests {
		return nil, errors.New("invalid Responses application configuration")
	}
	digest := sha256.Sum256(opts.DigestKey)
	s := &Service{store: opts.Store, executor: opts.Executor, archive: opts.Archive, resolve: opts.Resolve,
		digest: bytes.Clone(opts.DigestKey), version: hex.EncodeToString(digest[:]), retention: opts.Retention, limits: opts.Limits,
		maxActive: opts.ActiveRequests, maxOwner: opts.OwnerRequests, owners: make(map[uint]int)}
	if err := s.store.InitializeResponseRequests(ctx, s.version); err != nil {
		return nil, errors.New("Responses idempotency namespace is unavailable")
	}
	return s, nil
}

func (s *Service) acquire(owner uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.maxActive || s.owners[owner] >= s.maxOwner {
		return false
	}
	s.active++
	s.owners[owner]++
	return true
}

func (s *Service) release(owner uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	s.owners[owner]--
	if s.owners[owner] == 0 {
		delete(s.owners, owner)
	}
}

func (s *Service) hash(domain string, data []byte) string {
	h := hmac.New(sha256.New, s.digest)
	_, _ = h.Write([]byte("TXAPI/responses/" + domain + "/v1\x00"))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func canonical(body []byte) ([]byte, error) {
	if err := jsonutil.Validate(body); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func randomID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 256 {
		return false
	}
	for _, c := range []byte(key) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func headers(w http.ResponseWriter, receipt model.ResponseRequest) {
	w.Header().Set("X-TXAPI-Generation-ID", receipt.ID)
	w.Header().Set("X-TXAPI-Response-ID", receipt.ResponseID)
}

func respond(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": kind, "message": message}})
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := access.PrincipalFromContext(r.Context())
	if !ok || p.UserID == 0 || p.TokenID == 0 {
		respond(w, 401, "unauthorized", "a verified identity is required")
		return
	}
	if !s.acquire(p.UserID) {
		w.Header().Set("Retry-After", "1")
		respond(w, 429, "rate_limit_error", "Responses capacity is busy")
		return
	}
	defer s.release(p.UserID)
	binding := access.Binding{UserID: p.UserID, TokenID: p.TokenID}
	current, err := s.resolve(r.Context(), binding)
	if errors.Is(err, access.ErrUnauthorized) {
		respond(w, 401, "unauthorized", "identity is no longer authorized")
		return
	}
	if err != nil || current.UserID != p.UserID || current.TokenID != p.TokenID || current.Policy.Validate() != nil ||
		current.RPMLimit < 0 || current.TPMLimit < 0 || current.UserRPMLimit < 0 || current.UserConcurrencyLimit < 0 || current.KeyConcurrencyLimit < 0 {
		respond(w, 503, "authorization_unavailable", "current identity cannot be verified")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil {
		respond(w, 400, "invalid_request_error", "could not read Responses body")
		return
	}
	if len(body) > MaxRequestBytes {
		respond(w, 413, "request_too_large", "Responses request exceeds 1 MiB")
		return
	}
	request, err := protocol.ParseRequest(protocol.Responses, body)
	if err != nil {
		respond(w, 400, "invalid_request_error", err.Error())
		return
	}
	if !current.Policy.Allows(request.Model()) {
		respond(w, 403, "forbidden", "model is not authorized")
		return
	}
	if len(request.Model()) > 256 {
		respond(w, 400, "invalid_request_error", "model name is too long")
		return
	}
	canonicalBody, err := canonical(body)
	if err != nil {
		respond(w, 400, "invalid_request_error", "invalid or ambiguous Responses JSON")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	key := r.Header.Get("Idempotency-Key")
	if len(keys) > 1 || (len(keys) == 1 && !validKey(key)) {
		respond(w, 400, "invalid_request_error", "Idempotency-Key must contain 1 to 256 visible ASCII bytes")
		return
	}
	id, err := randomID()
	if err != nil {
		respond(w, 503, "responses_unavailable", "could not allocate generation identity")
		return
	}
	if len(keys) == 0 {
		key = id
	}
	native, _ := request.Responses()
	now := time.Now()
	receipt := model.ResponseRequest{ID: id, UserID: p.UserID, TokenID: p.TokenID, Operation: string(protocol.Responses),
		KeyHash: s.hash("key", []byte(fmt.Sprintf("%d/%d/%s", p.UserID, p.TokenID, key))), RequestHash: s.hash("request", canonicalBody), DigestVersion: s.version,
		ResponseID: "resp_" + id, Model: request.Model(), Stream: request.Stream(), Store: native.StoreEnabled(), State: model.ResponseDispatching,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(s.retention).UnixMilli()}
	receipt, fresh, err := s.store.BeginResponseRequest(r.Context(), receipt, s.limits)
	if err != nil {
		switch {
		case errors.Is(err, access.ErrUnauthorized):
			respond(w, 401, "unauthorized", "identity is no longer authorized")
		case errors.Is(err, model.ErrResponseConflict):
			respond(w, 409, "idempotency_conflict", "Idempotency-Key was used for another request")
		case errors.Is(err, model.ErrResponseCapacity):
			respond(w, 429, "responses_capacity", "Responses receipt capacity is full")
		default:
			respond(w, 503, "responses_unavailable", "Responses receipt could not be recorded")
		}
		return
	}
	headers(w, receipt)
	ctx := access.WithPrincipal(r.Context(), current)
	if !fresh {
		s.replay(ctx, w, binding, receipt)
		return
	}
	w.Header().Set("X-TXAPI-Idempotency", "created")
	s.execute(ctx, w, binding, receipt, body)
}

func (s *Service) replay(ctx context.Context, w http.ResponseWriter, binding access.Binding, receipt model.ResponseRequest) {
	w.Header().Set("X-TXAPI-Idempotency", "replayed")
	switch receipt.State {
	case model.ResponseDispatching:
		w.Header().Set("Retry-After", "1")
		respond(w, 409, "idempotency_in_progress", "the original generation is still in progress")
		return
	case model.ResponseUncertain:
		respond(w, 409, "idempotency_unknown", "the original result is unknown; automatic re-execution is unsafe")
		return
	}
	if receipt.OutputRecordID == "" {
		respond(w, 409, "idempotency_result_unavailable", "the original generation is not re-executed; its body is not retained")
		return
	}
	stored, err := s.archive.Read(ctx, binding, receipt.OutputRecordID)
	if err != nil || !stored.Result.ProtocolComplete && receipt.State != model.ResponseRejected || stored.Result.Model != receipt.Model || stored.Result.Operation != protocol.Responses {
		respond(w, 409, "idempotency_result_unavailable", "the original generation is not re-executed; its body is unavailable")
		return
	}
	// Request tracing is per delivery; do not replay the first caller's trace ID.
	delete(stored.Metadata.Values, "X-Request-Id")
	delete(stored.Metadata.Values, "X-Request-ID")
	output := core.NewHTTPOutput(w)
	if err := output.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
	defer output.SetWriteDeadline(time.Time{})
	if ctx.Err() != nil {
		return
	}
	if err := output.Commit(ctx, stored.Metadata); err != nil {
		return
	}
	if n, err := output.Write(ctx, stored.Body); err != nil || n != len(stored.Body) {
		return
	}
	if receipt.Stream {
		_ = output.Flush(ctx)
	}
}

func (s *Service) finish(receipt model.ResponseRequest, completion model.ResponseRequestCompletion) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.store.FinishResponseRequest(ctx, receipt, completion)
}

func (s *Service) execute(ctx context.Context, w http.ResponseWriter, binding access.Binding, receipt model.ResponseRequest, body []byte) {
	output := core.NewHTTPOutput(w)
	var recording *generation.Foreground
	finished := false
	defer func() {
		panicked := recover() != nil
		if !finished {
			_ = s.finish(receipt, model.ResponseRequestCompletion{State: model.ResponseUncertain, ErrorType: "execution_unknown"})
		}
		if panicked {
			// A panic can follow dispatch. Never log payloads or make this key fresh.
			slog.Error("Responses execution interrupted", "generation_id", receipt.ID)
			if !output.State().Committed {
				respond(w, 500, "internal_error", "Responses execution was interrupted")
			}
		}
	}()
	var target core.Output = output
	if receipt.Store {
		var err error
		recording, err = s.archive.BeginForeground(ctx, binding, receipt.Model, output)
		if err != nil {
			finished = s.finish(receipt, model.ResponseRequestCompletion{State: model.ResponseRejected, StatusCode: 503, ErrorType: "archive_unavailable"}) == nil
			respond(w, 503, "archive_unavailable", "encrypted replay capacity is unavailable")
			return
		}
		target = recording
	}
	result := s.executor.ExecuteTo(ctx, target, gateway.Execution{Operation: protocol.Responses, Body: body, ResponseID: receipt.ResponseID})
	completion := model.ResponseRequestCompletion{State: model.ResponseUncertain, NativeStatus: result.ResponseStatus, UpstreamResponseID: result.UpstreamResponseID,
		StatusCode: result.StatusCode, HTTPStatus: output.State().HTTPStatus}
	if result.Error != nil {
		completion.ErrorType = result.Error.Error.Type
	}
	if len(completion.UpstreamResponseID) > 256 {
		completion.UpstreamResponseID = ""
	}
	if result.ProtocolComplete && result.Termination != core.ClientWriteError {
		completion.State = model.ResponseCompleted
		if result.ResponseStatus == "failed" || result.ResponseStatus == "cancelled" || result.StatusCode >= 400 {
			completion.State = model.ResponseFailed
		}
	} else {
		notDispatched := result.Phase == core.NotDispatched
		for _, attempt := range result.Attempts {
			notDispatched = notDispatched && attempt.Phase == core.NotDispatched
		}
		if notDispatched {
			completion.State = model.ResponseRejected
		}
	}
	if recording != nil {
		if err := recording.Finish(result); err == nil && completion.State != model.ResponseUncertain {
			completion.OutputRecordID = recording.ID()
		} else if err != nil && completion.State != model.ResponseRejected {
			completion.State = model.ResponseUncertain
		}
	}
	if err := s.finish(receipt, completion); err != nil {
		slog.Error("Responses receipt finalization failed", "generation_id", receipt.ID)
		if !output.State().Committed {
			respond(w, 503, "responses_unavailable", "generation result could not be finalized")
		}
		return
	}
	finished = true
	if !output.State().Committed {
		respond(w, 503, "responses_unavailable", "generation output is unavailable; the request is not automatically re-executed")
	}
}
