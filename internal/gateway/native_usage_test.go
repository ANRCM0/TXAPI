package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"minigateway/internal/access"
	codec "minigateway/internal/compat/responsesws"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func enableNativeUsage(f *nativeServiceFixture, limit int) {
	f.service.gateway.usageHistory = f.store.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: limit, OwnerRecords: limit, RetentionSeconds: 3600})
	f.channels[0].GroupName = "default"
	f.service.options.UsageDecoder = func(model string) func(v2.Frame) (v3.Envelope, error) { return codec.NewDecoder(model).DecodeUsage }
}

func TestNativeUsageDurableSnapshotsAndIndependentRounds(t *testing.T) {
	for _, tc := range []struct {
		name, usage, status                     string
		input, output, total, cached, reasoning *int64
		completeness                            v2.Completeness
	}{
		{"complete", `{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":0}}`, "completed", usageInt(2), usageInt(1), usageInt(3), usageInt(1), usageInt(0), v2.Complete},
		{"zero", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, "completed", usageInt(0), usageInt(0), usageInt(0), nil, nil, v2.Complete},
		{"partial", `{"input_tokens":2}`, "completed", usageInt(2), nil, nil, nil, nil, v2.Partial},
		{"unknown", `null`, "completed", nil, nil, nil, nil, nil, v2.Unknown},
		{"incomplete", `{"input_tokens":2}`, "incomplete", usageInt(2), nil, nil, nil, nil, v2.Partial},
		{"failed", `null`, "failed", nil, nil, nil, nil, nil, v2.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeServiceFixture(t)
			enableNativeUsage(f, 5)
			f.service.options.UsageDecoder = func(name string) func(v2.Frame) (v3.Envelope, error) {
				decoder := codec.NewDecoder(name)
				return func(frame v2.Frame) (v3.Envelope, error) {
					var root map[string]json.RawMessage
					_ = json.Unmarshal(frame.Data, &root)
					if string(root["type"]) == `"response.completed"` {
						var response map[string]json.RawMessage
						_ = json.Unmarshal(root["response"], &response)
						response["usage"] = json.RawMessage(tc.usage)
						response["status"], _ = json.Marshal(tc.status)
						if tc.status == "incomplete" {
							response["incomplete_details"] = json.RawMessage(`{"reason":"max_output_tokens"}`)
						}
						if tc.status == "failed" {
							response["error"] = json.RawMessage(`{"code":"fixture_error","message":"private-upstream-message"}`)
						}
						root["response"], _ = json.Marshal(response)
						root["type"], _ = json.Marshal("response." + tc.status)
						frame.Data, _ = json.Marshal(root)
					}
					return decoder.DecodeUsage(frame)
				}
			}
			f.publish(t)
			s := f.open(t, "usage-rounds")
			count := 1
			if tc.status == "completed" {
				count = 2
			}
			ids := map[string]bool{}
			for round := 0; round < count; round++ {
				previous := ""
				if round > 0 && tc.completeness == v2.Complete {
					previous = "resp-1"
				}
				result := s.Execute(access.WithRequestID(f.trusted, "socket-correlation"), nativeCreate(previous), nativeDiscard)
				if (result.Problem != nil) != (tc.status != "completed") || result.UsageRecordID == "" || result.RecordingState != "sealed" || ids[result.UsageRecordID] {
					t.Fatalf("round result: %+v", result)
				}
				ids[result.UsageRecordID] = true
				var row model.UsageInvocation
				var attempt model.UsageAttemptReceipt
				if err := f.store.DB().Where("id = ?", result.UsageRecordID).First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.store.DB().Where("invocation_id = ?", result.UsageRecordID).First(&attempt).Error; err != nil {
					t.Fatal(err)
				}
				host := attempt.Host
				if row.Transport != "websocket" || row.UserID != f.binding.UserID || row.TokenID != f.binding.TokenID || row.RequestID != "socket-correlation" || row.AttemptCount != 1 || row.MaxAttempts != 1 || row.State != "sealed" || !row.Stream || host.Phase != v1.ResponseReceived || host.HTTPStatus != 0 || host.Status != 0 || host.UpstreamStatus != 0 || !host.ProtocolComplete || !host.DeliveryCommitted || host.BytesWritten == 0 || !attempt.MayDispatch || attempt.State != "observed" {
					t.Fatalf("row=%+v attempt=%+v", row, attempt)
				}
				wantTerm := v1.Success
				if tc.status == "failed" {
					wantTerm = v1.UpstreamError
				}
				if host.Termination != wantTerm || host.ProtocolStatus != tc.status {
					t.Fatal(host)
				}
				got := []*int64{attempt.InputTokens, attempt.OutputTokens, attempt.TotalTokens, attempt.CachedInputTokens, attempt.ReasoningOutputTokens}
				want := []*int64{tc.input, tc.output, tc.total, tc.cached, tc.reasoning}
				for i, n := range got {
					if (n == nil) != (want[i] == nil) || n != nil && *n != *want[i] {
						t.Fatalf("quantity %d: %v want %v", i, n, want[i])
					}
				}
				if attempt.Completeness != string(tc.completeness) {
					t.Fatal(attempt.Completeness)
				}
			}
			if f.resources[0].dials.Load() != 1 || f.resources[0].conn.count() != count {
				t.Fatal("rounds were redialed or resent")
			}
		})
	}
}
func usageInt(n int64) *int64 { return &n }

func TestNativeUsageRecordingFailuresDoNotSendOrRewriteDeliveredResult(t *testing.T) {
	for _, stage := range []string{"begin", "prepare", "dispatch", "observe", "finish"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/panic=%v", stage, panics), func(t *testing.T) {
				f := newNativeServiceFixture(t)
				probe := &historyRecorderProbe{}
				if panics {
					probe.panicAt = stage
				} else {
					probe.fail = stage
				}
				f.service.gateway.usageHistory = probe
				f.publish(t)
				s := f.open(t, "record-failure")
				r := s.Execute(f.trusted, nativeCreate(""), nativeDiscard)
				sent := stage == "observe" || stage == "finish"
				if (r.Problem == nil) != sent || r.Facts.UpstreamWriteStarted != sent || f.resources[0].conn.count() != map[bool]int{true: 1, false: 0}[sent] {
					t.Fatalf("record failure facts: %+v", r)
				}
				if sent && (!r.Facts.ProtocolComplete || r.Facts.Termination != v2.Success) {
					t.Fatal("record failure rewrote generation")
				}
				if r.RecordingState == "sealed" || r.RecordingState == "open" {
					t.Fatal("record failure reported saved success")
				}
				if stage == "begin" {
					if r.UsageRecordID != "" {
						t.Fatal("failed Begin exposed accepted record")
					}
				} else if r.UsageRecordID == "" {
					t.Fatal("accepted record identity lost")
				}
				if len(probe.starts) != 1 || probe.starts[0].Transport != "websocket" || probe.starts[0].MaxAttempts != 1 {
					t.Fatal(probe.starts)
				}
			})
		}
	}
}

func TestNativeUsageCancellationDeadlineAndFailedDeliveryRemainUnknown(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "write", "panic"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeServiceFixture(t)
			enableNativeUsage(f, 2)
			if mode == "cancel" || mode == "deadline" {
				f.block = make(chan struct{})
			}
			if mode == "deadline" {
				f.service.options.Limits.RoundTimeout = 30 * time.Millisecond
			}
			f.publish(t)
			s := f.open(t, "interrupted-usage")
			ctx, cancel := context.WithCancel(f.trusted)
			defer cancel()
			deliver := nativeDiscard
			if mode == "write" {
				deliver = func(context.Context, v2.Frame) (int, error) { return 0, errors.New("client closed") }
			}
			if mode == "panic" {
				deliver = func(context.Context, v2.Frame) (int, error) { panic("private delivery panic") }
			}
			result := make(chan contract.Result, 1)
			go func() { result <- s.Execute(ctx, nativeCreate(""), deliver) }()
			if mode == "cancel" {
				select {
				case <-f.resources[0].conn.entered:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("round not entered")
				}
			}
			var r contract.Result
			select {
			case r = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("round did not join")
			}
			if r.Problem == nil || !r.Facts.UpstreamWriteStarted || r.Facts.ProtocolComplete {
				t.Fatalf("failure facts %+v", r)
			}
			var row model.UsageInvocation
			var attempt model.UsageAttemptReceipt
			if err := f.store.DB().Where("id = ?", r.UsageRecordID).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.store.DB().Where("invocation_id = ?", r.UsageRecordID).First(&attempt).Error; err != nil {
				t.Fatal(err)
			}
			term := map[string]v1.Termination{"cancel": v1.Canceled, "deadline": v1.Deadline, "write": v1.ClientWriteError, "panic": v1.UpstreamError}[mode]
			if row.Host.Termination != term || attempt.Host.Termination != term || attempt.TotalTokens != nil || attempt.Completeness != string(v2.Unknown) || attempt.Host.ProtocolComplete || f.resources[0].conn.count() != 1 {
				t.Fatalf("invented failure usage: %+v %+v", row, attempt)
			}
			if mode == "panic" && row.State != "interrupted" {
				t.Fatal("panic did not interrupt recording")
			}
		})
	}
}

func TestNativeUsageCapacityAndShutdownAdmissionFailBeforeNextRoundSend(t *testing.T) {
	f := newNativeServiceFixture(t)
	enableNativeUsage(f, 1)
	f.publish(t)
	s := f.open(t, "usage-capacity")
	first := s.Execute(f.trusted, nativeCreate(""), nativeDiscard)
	if first.Problem != nil {
		t.Fatal(first.Problem)
	}
	second := s.Execute(f.trusted, nativeCreate("resp-1"), nativeDiscard)
	if second.Problem == nil || second.RecordingState != "unavailable" || second.Facts.UpstreamWriteStarted || f.resources[0].conn.count() != 1 {
		t.Fatalf("capacity bypass %+v", second)
	}
	f.service.gateway.recordingMu.Lock()
	f.service.gateway.recordingClosed = true
	f.service.gateway.recordingMu.Unlock()
	if _, err := f.service.gateway.beginNativeUsage(f.trusted, "alias"); err != history.ErrUnavailable {
		t.Fatal(err)
	}
}

func TestNativeUsageFinishSurvivesRevocationAndCloseJoins(t *testing.T) {
	f := newNativeServiceFixture(t)
	enableNativeUsage(f, 2)
	f.block = make(chan struct{})
	f.publish(t)
	s := f.open(t, "usage-shutdown")
	ctx, cancel := context.WithCancel(f.trusted)
	done := make(chan struct{})
	go func() { defer close(done); s.Execute(ctx, nativeCreate(""), nativeDiscard) }()
	select {
	case <-f.resources[0].conn.entered:
	case <-time.After(time.Second):
		t.Fatal("not running")
	}
	if err := f.store.DB().Model(&model.Token{}).Where("id = ?", f.binding.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	cancel()
	closeDone := make(chan error, 1)
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		closeDone <- f.service.Close(ctx)
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("service did not join")
	}
	<-done
	var rows []model.UsageInvocation
	if err := f.store.DB().Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].State != "sealed" || rows[0].Host.Termination != v1.Canceled {
		t.Fatal("revoked identity lost its final facts", rows, err)
	}
}

func TestNativeUsageGatewayCloseWaitsForRecordingAndRejectsNewRegistration(t *testing.T) {
	f := newNativeServiceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	probe := &historyRecorderProbe{hook: func(ctx context.Context, event string) {
		if event == "observe" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}}
	g := f.service.gateway
	g.usageHistory = probe
	f.publish(t)
	s := f.open(t, "record-join")
	result := make(chan contract.Result, 1)
	go func() { result <- s.Execute(f.trusted, nativeCreate(""), nativeDiscard) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Observe not entered")
	}
	closing := make(chan struct{})
	go func() { g.Close(); close(closing) }()
	deadline := time.Now().Add(time.Second)
	for {
		g.recordingMu.Lock()
		closed := g.recordingClosed
		g.recordingMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway did not close recording admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closing:
		t.Fatal("gateway closed before recording joined")
	default:
	}
	if _, err := g.beginNativeUsage(f.trusted, "alias"); err != history.ErrUnavailable {
		t.Fatal("closed recording accepted new work", err)
	}
	unblock()
	select {
	case r := <-result:
		if r.Problem != nil || r.RecordingState != "sealed" {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("round did not join")
	}
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("gateway did not join final recording")
	}
}

func TestNativeUsageDifferentKeysAndConnectionsKeepIndependentInvocations(t *testing.T) {
	f := newNativeServiceFixture(t)
	enableNativeUsage(f, 4)
	f.publish(t)
	second, err := f.store.IssueToken(f.binding.UserID, &store.TokenInput{ModelMode: identity.All})
	if err != nil {
		t.Fatal(err)
	}
	secondContext, _, ok := authority.NewTokenAuthenticator(f.store).Authenticate(context.Background(), second.Key)
	if !ok {
		t.Fatal("second key failed authentication")
	}
	a := f.open(t, "first-key")
	b, problem := f.service.Open(secondContext, contract.Open{Binding: identity.Binding{UserID: f.binding.UserID, TokenID: second.ID}, ConnectionID: "second-key"})
	if problem != nil {
		t.Fatal(problem)
	}
	for i, item := range []struct {
		session contract.Session
		ctx     context.Context
		token   uint
	}{{a, f.trusted, f.binding.TokenID}, {b, secondContext, second.ID}} {
		result := item.session.Execute(access.WithRequestID(item.ctx, "same-socket-request-id"), nativeCreate(""), nativeDiscard)
		if result.Problem != nil || result.RecordingState != "sealed" {
			t.Fatal(result)
		}
		var row model.UsageInvocation
		if err := f.store.DB().First(&row, "id = ?", result.UsageRecordID).Error; err != nil || row.TokenID != item.token {
			t.Fatal(i, row, err)
		}
	}
	var count int64
	if err := f.store.DB().Model(&model.UsageInvocation{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
