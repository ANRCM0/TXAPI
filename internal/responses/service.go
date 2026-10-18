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
	application "minigateway/internal/foundation/contracts/responses/v1"

	"log/slog"

	"reflect"
	"sync"
	"time"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"

	native "minigateway/internal/compat/responses/v1"
	jsonutil "minigateway/internal/foundation/contracts/json/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

const MaxRequestBytes = application.MaxRequestBytes

type ReceiptStore = state.ReceiptStore

type Executor = execv1.Executor

type Options struct {
	History  *History
	Store    ReceiptStore
	Executor Executor
	Archive  state.Archive
	Identity identity.Authority

	DigestKey                     []byte
	Retention                     time.Duration
	Limits                        state.ResponseRequestLimits
	ActiveRequests, OwnerRequests int
}

type Service struct {
	readWindows map[uint]historyReadWindow
	history     *History
	store       ReceiptStore
	executor    Executor
	archive     state.Archive
	identity    identity.Authority

	digest                      []byte
	version                     string
	retention                   time.Duration
	limits                      state.ResponseRequestLimits
	mu                          sync.Mutex
	active, maxActive, maxOwner int
	owners                      map[uint]int
}

func missing(v any) bool {
	if v == nil {
		return true
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(v).IsNil()
	}
	return false
}

func New(ctx context.Context, opts Options) (*Service, error) {
	if missing(opts.Store) || missing(opts.Executor) || missing(opts.Archive) || missing(opts.Identity) || len(opts.DigestKey) != 32 ||
		opts.Retention < time.Minute || opts.Retention > 7*24*time.Hour || opts.Limits.OwnerRecords < 1 || opts.Limits.InstanceRecords <= opts.Limits.OwnerRecords ||
		opts.ActiveRequests < 2 || opts.ActiveRequests > 128 || opts.OwnerRequests < 1 || opts.OwnerRequests >= opts.ActiveRequests {
		return nil, errors.New("invalid Responses application configuration")
	}
	digest := sha256.Sum256(opts.DigestKey)
	s := &Service{history: opts.History, store: opts.Store, executor: opts.Executor, archive: opts.Archive, identity: opts.Identity,
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

func identify(delivery application.Delivery, receipt state.ResponseRequest) {
	delivery.Annotate(application.Metadata{GenerationID: receipt.ID, ResponseID: receipt.ResponseID})
}
func problem(delivery application.Delivery, code, message string) {
	delivery.Problem(application.Problem{Code: code, Message: message})
}

type requestCall struct {
	mu        sync.Mutex
	service   *Service
	ctx       context.Context
	principal identity.Principal
	binding   identity.Binding
	used      bool
	closed    bool
	action    application.Action
}

func (s *Service) Begin(parent context.Context, action application.Action) (application.Call, *application.Problem) {
	p, ok := s.identity.Current(parent)
	if !ok || p.UserID == 0 || p.TokenID == 0 {
		return nil, &application.Problem{Code: "unauthorized", Message: "a verified identity is required"}
	}
	if !s.acquire(p.UserID) {
		return nil, &application.Problem{Code: "rate_limit_error", Message: "Responses capacity is busy", Metadata: application.Metadata{RetryAfterSeconds: 1}}
	}
	handedOff := false
	defer func() {
		if !handedOff {
			s.release(p.UserID)
		}
	}()
	fail := func(code, message string, metadata application.Metadata) (application.Call, *application.Problem) {
		return nil, &application.Problem{Code: code, Message: message, Metadata: metadata}
	}
	binding := identity.Binding{UserID: p.UserID, TokenID: p.TokenID}
	ctx, current, err := s.identity.Refresh(parent, binding)
	if errors.Is(err, identity.ErrUnauthorized) {
		return fail("unauthorized", "identity is no longer authorized", application.Metadata{})
	}
	if err != nil || current.UserID != p.UserID || current.TokenID != p.TokenID || current.Policy.Validate() != nil || current.RPMLimit < 0 || current.TPMLimit < 0 || current.UserRPMLimit < 0 || current.UserConcurrencyLimit < 0 || current.KeyConcurrencyLimit < 0 {
		return fail("authorization_unavailable", "current identity cannot be verified", application.Metadata{})
	}
	if action != application.CreateResponse {
		if s.history == nil {
			return fail("history_unavailable", "Responses history is not enabled", application.Metadata{})
		}
		if !s.allowHistoryRead(binding.UserID) {
			return fail("rate_limit_error", "history delivery rate exceeded", application.Metadata{NoStore: true, RetryAfterSeconds: 60})
		}
	}
	call := &requestCall{service: s, ctx: ctx, principal: current, binding: binding, action: action}
	handedOff = true
	return call, nil
}
func (c *requestCall) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.service.release(c.binding.UserID)
	}
}
func (c *requestCall) Handle(command application.Command, delivery application.Delivery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.used {
		if !missing(delivery) {
			problem(delivery, "request_closed", "this request scope cannot be reused")
		}
		return
	}
	c.used = true
	if missing(delivery) {
		return
	}
	if command.Action != c.action {
		problem(delivery, "request_closed", "this request scope cannot be reused")
		return
	}
	if command.ExpectedVersion != nil {
		value := *command.ExpectedVersion
		command.ExpectedVersion = &value
	}
	command.IdempotencyKeys = append([]string(nil), command.IdempotencyKeys...)
	if command.Action != application.CreateResponse {
		c.service.historyCommand(c.ctx, delivery, command, c.binding)
		return
	}
	c.service.create(c.ctx, delivery, c.principal, c.binding, command)
}

func readPayload(command application.Command) ([]byte, error) {
	if missing(command.Payload) {
		return nil, nil
	}
	body, err := command.Payload.Read()
	if len(body) > MaxRequestBytes+1 {
		body = body[:MaxRequestBytes+1]
	}
	return bytes.Clone(body), err
}

func (s *Service) create(ctx context.Context, delivery application.Delivery, current identity.Principal, binding identity.Binding, command application.Command) {
	body, readErr := readPayload(command)
	if readErr != nil {
		problem(delivery, "invalid_request_error", "could not read Responses body")
		return
	}
	if len(body) > MaxRequestBytes {
		problem(delivery, "request_too_large", "Responses request exceeds 1 MiB")
		return
	}
	validationBody := body
	if s.history != nil {
		fields, _, _, parseErr := stateFields(body)
		if parseErr != nil {
			problem(delivery, "invalid_request_error", parseErr.Error())
			return
		}
		validationBody, _ = json.Marshal(fields)
	}
	request, err := native.ParseHTTP(validationBody)
	if err != nil {
		problem(delivery, "invalid_request_error", err.Error())
		return
	}
	if !current.Policy.Allows(request.Model) {
		problem(delivery, "forbidden", "model is not authorized")
		return
	}
	if len(request.Model) > 256 {
		problem(delivery, "invalid_request_error", "model name is too long")
		return
	}
	canonicalBody, err := canonical(body)
	if err != nil {
		problem(delivery, "invalid_request_error", "invalid or ambiguous Responses JSON")
		return
	}
	keys := command.IdempotencyKeys
	key := ""
	if len(keys) > 0 {
		key = keys[0]
	}
	if len(keys) > 1 || (len(keys) == 1 && !validKey(key)) {
		problem(delivery, "invalid_request_error", "Idempotency-Key must contain 1 to 256 visible ASCII bytes")
		return
	}
	id, err := randomID()
	if err != nil {
		problem(delivery, "responses_unavailable", "could not allocate generation identity")
		return
	}
	if len(keys) == 0 {
		key = id
	}
	now := time.Now()
	receipt := state.ResponseRequest{ID: id, UserID: binding.UserID, TokenID: binding.TokenID, Operation: string(execv1.Responses),
		KeyHash: s.hash("key", []byte(fmt.Sprintf("%d/%d/%s", binding.UserID, binding.TokenID, key))), RequestHash: s.hash("request", canonicalBody), DigestVersion: s.version,
		ResponseID: "resp_" + id, Model: request.Model, Stream: request.Stream, Store: request.StoreEnabled(), State: state.ResponseDispatching,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(s.retention).UnixMilli()}
	receipt, fresh, err := s.store.BeginResponseRequest(ctx, receipt, s.limits)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrUnauthorized):
			problem(delivery, "unauthorized", "identity is no longer authorized")
		case errors.Is(err, state.ErrResponseConflict):
			problem(delivery, "idempotency_conflict", "Idempotency-Key was used for another request")
		case errors.Is(err, state.ErrResponseCapacity):
			problem(delivery, "responses_capacity", "Responses receipt capacity is full")
		default:
			problem(delivery, "responses_unavailable", "Responses receipt could not be recorded")
		}
		return
	}
	identify(delivery, receipt)
	// Refresh returned the authenticated context; the public snapshot is not proof.
	if !fresh {
		if s.history != nil && receipt.Store && receipt.State != state.ResponseDispatching && receipt.State != state.ResponseUncertain && receipt.State != state.ResponseRejected {
			_, content, err := s.history.read(ctx, binding, receipt.ResponseID)
			if err != nil || !content.Ready {
				problem(delivery, "idempotency_result_unavailable", "retained response history is unavailable")
				return
			}
		}
		s.replay(ctx, delivery, binding, receipt)
		return
	}
	delivery.Annotate(application.Metadata{Idempotency: "created"})
	var turn *historyTurn
	if s.history != nil {
		body, turn, err = s.history.prepare(ctx, binding, receipt, body)
		if err != nil {
			_ = s.finish(receipt, state.ResponseRequestCompletion{State: state.ResponseRejected, StatusCode: 409, ErrorType: "history_unavailable"})
			historyProblem(delivery, err)
			return
		}
	}
	s.execute(ctx, delivery, binding, receipt, body, turn)
}

func (s *Service) replay(ctx context.Context, delivery application.Delivery, binding identity.Binding, receipt state.ResponseRequest) {
	delivery.Annotate(application.Metadata{Idempotency: "replayed"})
	switch receipt.State {
	case state.ResponseDispatching:
		delivery.Annotate(application.Metadata{RetryAfterSeconds: 1})
		problem(delivery, "idempotency_in_progress", "the original generation is still in progress")
		return
	case state.ResponseUncertain:
		problem(delivery, "idempotency_unknown", "the original result is unknown; automatic re-execution is unsafe")
		return
	}
	if receipt.OutputRecordID == "" {
		problem(delivery, "idempotency_result_unavailable", "the original generation is not re-executed; its body is not retained")
		return
	}
	stored, err := s.archive.Read(ctx, binding, receipt.OutputRecordID)
	if err != nil || !stored.Result.ProtocolComplete && receipt.State != state.ResponseRejected || stored.Result.Model != receipt.Model || stored.Result.Operation != execv1.Responses {
		problem(delivery, "idempotency_result_unavailable", "the original generation is not re-executed; its body is unavailable")
		return
	}
	// Request tracing is per delivery; do not replay the first caller's trace ID.
	stored.Metadata = native.ReplayMetadata(stored.Metadata)
	output := delivery
	if err := output.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, execv1.ErrDeadlineUnsupported) {
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

func (s *Service) finish(receipt state.ResponseRequest, completion state.ResponseRequestCompletion) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.store.FinishResponseRequest(ctx, receipt, completion)
}

func (s *Service) execute(ctx context.Context, delivery application.Delivery, binding identity.Binding, receipt state.ResponseRequest, body []byte, turn *historyTurn) {
	output := delivery
	var recording state.Foreground
	finished := false
	historyFinished := turn == nil
	defer func() {
		panicked := recover() != nil
		if !historyFinished {
			_ = s.history.finish(binding, turn, execv1.Outcome{Result: execv1.Result{Phase: execv1.ResponseReceived}}, &captureOutput{}, receipt.Stream, false)
		}
		if !finished {
			_ = s.finish(receipt, state.ResponseRequestCompletion{State: state.ResponseUncertain, ErrorType: "execution_unknown"})
		}
		if panicked {
			// A panic can follow dispatch. Never log payloads or make this key fresh.
			slog.Error("Responses execution interrupted", "generation_id", receipt.ID)
			if !output.State().Committed {
				problem(delivery, "internal_error", "Responses execution was interrupted")
			}
		}
	}()
	var target execv1.Output = output
	if receipt.Store {
		var err error
		recording, err = s.archive.BeginForeground(ctx, binding, receipt.Model, output)
		if err != nil {
			if turn != nil {
				_ = s.history.finish(binding, turn, execv1.Outcome{Result: execv1.Result{Phase: execv1.NotDispatched}}, &captureOutput{}, receipt.Stream, false)
				historyFinished = true
			}
			finished = s.finish(receipt, state.ResponseRequestCompletion{State: state.ResponseRejected, StatusCode: 503, ErrorType: "archive_unavailable"}) == nil
			problem(delivery, "archive_unavailable", "encrypted replay capacity is unavailable")
			return
		}
		target = recording
	}
	var captured *captureOutput
	if turn != nil {
		captured = &captureOutput{Output: target, parent: turn.content.Parent, conversation: turn.content.Conversation}
		target = captured
	}
	input := execv1.Invocation{Operation: execv1.Responses, Body: body, OutputID: receipt.ResponseID}
	if turn != nil {
		input.Target = turn.target
	}
	result := s.executor.Execute(ctx, target, input)
	completion := state.ResponseRequestCompletion{State: state.ResponseUncertain, NativeStatus: result.Native.Status, UpstreamResponseID: result.Native.ID,
		StatusCode: result.StatusCode, HTTPStatus: output.State().HTTPStatus}
	if result.Error != nil {
		completion.ErrorType = result.Error.Error.Type
	}
	if len(completion.UpstreamResponseID) > 256 {
		completion.UpstreamResponseID = ""
	}
	if result.ProtocolComplete && result.Termination != execv1.ClientWriteError {
		completion.State = state.ResponseCompleted
		if result.Native.Status == "failed" || result.Native.Status == "cancelled" || result.StatusCode >= 400 {
			completion.State = state.ResponseFailed
		}
	} else {
		notDispatched := result.Phase == execv1.NotDispatched
		for _, attempt := range result.Attempts {
			notDispatched = notDispatched && attempt.Phase == execv1.NotDispatched
		}
		if notDispatched {
			completion.State = state.ResponseRejected
		}
	}
	if recording != nil {
		if err := recording.Finish(result); err == nil && completion.State != state.ResponseUncertain {
			completion.OutputRecordID = recording.ID()
		} else if err != nil && completion.State != state.ResponseRejected {
			completion.State = state.ResponseUncertain
		}
	}
	if turn != nil {
		known := completion.State == state.ResponseCompleted || completion.State == state.ResponseFailed
		if err := s.history.finish(binding, turn, result, captured, receipt.Stream, known); err != nil {
			completion.State = state.ResponseUncertain
			completion.OutputRecordID = ""
		}
		historyFinished = true
	}
	if err := s.finish(receipt, completion); err != nil {
		slog.Error("Responses receipt finalization failed", "generation_id", receipt.ID)
		if !output.State().Committed {
			problem(delivery, "responses_unavailable", "generation result could not be finalized")
		}
		return
	}
	finished = true
	if !output.State().Committed {
		problem(delivery, "responses_unavailable", "generation output is unavailable; the request is not automatically re-executed")
	}
}

var _ application.Service = (*Service)(nil)
