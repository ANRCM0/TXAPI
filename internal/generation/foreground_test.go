package generation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"minigateway/internal/core"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol"
)

type foregroundTarget struct {
	state    core.OutputState
	onCommit func() error
	onWrite  func([]byte) (int, error)
	onFlush  func() error
}

func (o *foregroundTarget) Commit(context.Context, core.ResponseMetadata) error {
	if o.onCommit != nil {
		if err := o.onCommit(); err != nil {
			return err
		}
	}
	o.state = core.OutputState{Committed: true, HTTPStatus: 200}
	return nil
}
func (o *foregroundTarget) Write(_ context.Context, data []byte) (int, error) {
	if o.onWrite != nil {
		return o.onWrite(data)
	}
	return len(data), nil
}
func (o *foregroundTarget) Flush(context.Context) error {
	if o.onFlush != nil {
		return o.onFlush()
	}
	return nil
}
func (o *foregroundTarget) State() core.OutputState { return o.state }

func foregroundResult() gateway.RequestResult {
	return gateway.RequestResult{Operation: protocol.Responses, Result: core.Result{StatusCode: 200, Phase: core.ResponseReceived, Termination: core.Success, ProtocolComplete: true}}
}

func TestForegroundPersistsEncryptedOutputBeforeDownstreamAndSealsNativeFailures(t *testing.T) {
	for _, failed := range []bool{false, true} {
		st, binding, _ := fixture(t)
		a := testArchive(t, archiveOptions(st))
		var foreground *Foreground
		secret := []byte("foreground-output-secret")
		target := &foregroundTarget{}
		target.onCommit = func() error {
			record, err := st.ReadGenerationRecord(context.Background(), binding, foreground.ID())
			if err != nil || len(record.Segments) != 1 || record.Segments[0].Kind != "metadata" {
				t.Error("downstream commit preceded durable metadata")
			}
			return nil
		}
		target.onWrite = func(data []byte) (int, error) {
			record, err := st.ReadGenerationRecord(context.Background(), binding, foreground.ID())
			if err != nil || len(record.Segments) != 2 || record.Segments[1].Kind != "body" {
				t.Error("downstream write preceded durable body")
			}
			if len(record.Segments) > 1 && bytes.Contains(record.Segments[1].Ciphertext, secret) {
				t.Error("foreground body was persisted in plaintext")
			}
			return len(data), nil
		}
		var err error
		foreground, err = a.BeginForeground(context.Background(), binding, "m", target)
		if err != nil {
			t.Fatal(err)
		}
		if err := foreground.Commit(context.Background(), core.ResponseMetadata{StatusCode: 200, ContentType: "application/json"}); err != nil {
			t.Fatal(err)
		}
		if _, err := foreground.Write(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
		if err := foreground.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		result := foregroundResult()
		if failed {
			result.StatusCode, result.Termination = 502, core.UpstreamError
			result.ResponseStatus = "failed"
		}
		if err := foreground.Finish(result); err != nil {
			t.Fatal(err)
		}
		recording, err := a.Read(context.Background(), binding, foreground.ID())
		if err != nil || !bytes.Equal(recording.Body, secret) || recording.Result.StatusCode != result.StatusCode {
			t.Fatalf("complete native output could not replay: %v", err)
		}
		if state := foreground.State(); !state.Committed || state.HTTPStatus != 200 {
			t.Fatalf("downstream HTTP commitment lost: %+v", state)
		}
	}
}

func TestForegroundIOFailureNeverSealsReplayableOutput(t *testing.T) {
	for _, failure := range []string{"commit", "write", "short_write", "flush", "panic", "unknown_execution"} {
		t.Run(failure, func(t *testing.T) {
			st, binding, _ := fixture(t)
			a := testArchive(t, archiveOptions(st))
			target := &foregroundTarget{}
			switch failure {
			case "commit":
				target.onCommit = func() error { return io.ErrClosedPipe }
			case "write":
				target.onWrite = func([]byte) (int, error) { return 0, io.ErrClosedPipe }
			case "short_write":
				target.onWrite = func([]byte) (int, error) { return 1, nil }
			case "flush":
				target.onFlush = func() error { return io.ErrClosedPipe }
			case "panic":
				target.onWrite = func([]byte) (int, error) { panic("private-output-error") }
			}
			foreground, err := a.BeginForeground(context.Background(), binding, "m", target)
			if err != nil {
				t.Fatal(err)
			}
			err = foreground.Commit(context.Background(), core.ResponseMetadata{StatusCode: 200})
			if err == nil {
				_, err = foreground.Write(context.Background(), []byte("payload"))
			}
			if err == nil {
				err = foreground.Flush(context.Background())
			}
			if failure != "unknown_execution" && err == nil {
				t.Fatal("output failure was swallowed")
			}
			if !foreground.State().Committed {
				t.Fatal("irreversible archive commitment allowed a retry")
			}
			result := foregroundResult()
			if failure == "unknown_execution" {
				result.ProtocolComplete = false
			}
			if err := foreground.Finish(result); !errors.Is(err, ErrArchiveIncomplete) {
				t.Fatalf("failed output sealed: %v", err)
			}
			if _, err := a.Read(context.Background(), binding, foreground.ID()); !errors.Is(err, ErrArchiveIncomplete) {
				t.Fatalf("failed output replayed: %v", err)
			}
		})
	}
}

func TestForegroundRejectsMissingOrCommittedTargetBeforeAllocatingRecord(t *testing.T) {
	st, binding, _ := fixture(t)
	a := testArchive(t, archiveOptions(st))
	var missing *memoryOutput
	for _, target := range []core.Output{nil, missing, &memoryOutput{state: core.OutputState{Committed: true}}} {
		if foreground, err := a.BeginForeground(context.Background(), binding, "m", target); err == nil || foreground != nil {
			t.Fatal("invalid target accepted")
		}
	}
}
