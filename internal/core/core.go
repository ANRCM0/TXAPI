// Package core executes one HTTP attempt. It has no framework, database, user,
// routing, billing, or protocol-specific dependencies.
package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    any    `json:"code,omitempty"`
}

type Error struct {
	StatusCode int
	Error      ErrorDetail
}

type Termination string

// Phase records local observations, never proof that the provider did not run.
type Phase string

const (
	NotDispatched    Phase = "not_dispatched"
	Dispatching      Phase = "dispatching"
	ResponseReceived Phase = "response_received"
)

const (
	Success          Termination = "success"
	UpstreamError    Termination = "upstream_error"
	Deadline         Termination = "deadline"
	Canceled         Termination = "canceled"
	ClientWriteError Termination = "client_write_error"
)

// Result reports execution independently of the HTTP status already sent.
// Committed is monotonic: irreversible output commitment cannot be reset.
type Result struct {
	TimeoutStage     TimeoutStage
	Phase            Phase
	UpstreamStatus   int
	ProtocolComplete bool
	StatusCode       int
	HTTPStatus       int
	Committed        bool
	Replayable       bool
	Termination      Termination
	Usage            *Usage
	Error            *Error
	Duration         time.Duration
	BytesWritten     int64
}

type Frame struct {
	EffectiveOutput bool
	EventComplete   bool
	// Deferred frame commit is handled solely by core, never by the decoder.
	Data  []byte
	Usage *Usage
	// Complete marks a protocol terminal event (e.g. OpenAI DONE/Claude stop).
	Complete bool
}

type StreamDecoder interface {
	DecodeLine([]byte) ([]Frame, error)
	End() ([]Frame, error)
}

type Decoder interface {
	Decode([]byte) ([]byte, *Usage, error)
	NewStream() StreamDecoder
}

type Input struct {
	// ObserveResponseHeaders runs once after a successful Do, before reading or
	// committing output. Headers are borrowed read-only for this call only.
	// The observer must not retain/mutate them, perform I/O, block or panic.
	ObserveResponseHeaders func(status int, headers http.Header)
	HTTPStageTimeouts      HTTPStageTimeouts
	Method                 string
	URL                    string
	Header                 http.Header
	Body                   []byte
	Stream                 bool
	Replayable             bool
	Timeout                time.Duration
	Decoder                Decoder
}

type Options struct {
	Client           *http.Client
	MaxResponseBytes int64
	MaxLineBytes     int
}

type Executor struct {
	client           *http.Client
	maxResponseBytes int64
	maxLineBytes     int
}

func New(opts Options) *Executor {
	client := opts.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 200
		transport.MaxIdleConnsPerHost = 50
		client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = 32 << 20
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = 1 << 20
	}
	return &Executor{client: client, maxResponseBytes: opts.MaxResponseBytes, maxLineBytes: opts.MaxLineBytes}
}

func (e *Executor) Close() { e.client.CloseIdleConnections() }

func failure(status int, message string, kind string) *Error {
	return &Error{StatusCode: status, Error: ErrorDetail{Message: message, Type: kind}}
}

func (e *Executor) Execute(parent context.Context, w http.ResponseWriter, in Input) (result Result) {
	return e.ExecuteTo(parent, NewHTTPOutput(w), in)
}

// ExecuteTo executes one HTTP attempt with an independently owned output.
func (e *Executor) ExecuteTo(parent context.Context, output Output, in Input) (result Result) {
	started := time.Now()
	result.Phase = NotDispatched
	result.Replayable = in.Replayable
	result.Termination = UpstreamError
	defer func() {
		result.Duration = time.Since(started)
		if output != nil {
			state := output.State()
			result.Committed, result.HTTPStatus = state.Committed, state.HTTPStatus
		}
	}()
	ctx := parent
	var cancel context.CancelFunc
	if in.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, in.Timeout)
		defer cancel()
	}
	var stages *httpStageWatchdog
	if in.HTTPStageTimeouts.enabled() {
		ctx, stages = newHTTPStageWatchdog(ctx, in.HTTPStageTimeouts)
		defer stages.close()
	}
	fail := func(message string, cause error, term Termination) {
		result.StatusCode = http.StatusBadGateway
		result.Termination = term
		if stage := stages.timeoutStage(); stage != "" {
			result.TimeoutStage = stage
			result.StatusCode, result.Termination = http.StatusGatewayTimeout, Deadline
			if stage == TimeoutDownstreamWrite {
				result.Termination = ClientWriteError
			}
			result.Error = failure(result.StatusCode, message, string(result.Termination))
			return
		}
		if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			if errors.Is(parent.Err(), context.Canceled) || errors.Is(cause, context.Canceled) {
				result.StatusCode = 499
				result.Termination = Canceled
			} else {
				result.StatusCode = http.StatusGatewayTimeout
				result.Termination = Deadline
			}
		}
		// Never expose request URLs (Gemini credentials may be in query strings).
		result.Error = failure(result.StatusCode, message, string(result.Termination))
	}
	if output == nil || in.Decoder == nil {
		result.StatusCode = http.StatusInternalServerError
		result.Error = failure(result.StatusCode, "missing output or response decoder", "internal_error")
		return
	}
	if output.State().Committed {
		result.StatusCode = http.StatusInternalServerError
		result.Error = failure(result.StatusCode, "execution output is already committed", "internal_error")
		return
	}
	if !in.HTTPStageTimeouts.valid() {
		result.StatusCode = http.StatusInternalServerError
		result.Error = failure(500, "invalid HTTP stage timeout", "internal_error")
		return
	}
	controller, supported := output.(interface{ SetWriteDeadline(time.Time) error })
	if in.HTTPStageTimeouts.DownstreamWrite > 0 {
		deadline, _ := ctx.Deadline()
		if !supported {
			result.StatusCode = http.StatusInternalServerError
			result.Error = failure(500, "output does not support write deadlines", "internal_error")
			return
		}
		if err := controller.SetWriteDeadline(deadline); err != nil {
			result.StatusCode = http.StatusInternalServerError
			result.Error = failure(500, "output does not support write deadlines", "internal_error")
			return
		}
		defer controller.SetWriteDeadline(time.Time{})
	}
	if err := ctx.Err(); err != nil {
		fail("request canceled before upstream execution", err, Canceled)
		return
	}
	method := in.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, in.URL, bytes.NewReader(in.Body))
	if err != nil {
		result.StatusCode = 400
		result.Error = failure(400, "invalid upstream request", "invalid_request")
		return
	}
	req.Header = in.Header.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	if in.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	// Once Do starts, a transport error cannot establish non-execution.
	result.Phase = Dispatching
	stages.headers(true, false)
	resp, err := e.client.Do(req)
	stages.headers(false, err == nil && resp.StatusCode == http.StatusOK && in.Stream)
	if err != nil {
		fail("upstream request failed", err, UpstreamError)
		return
	}
	defer resp.Body.Close()
	result.Phase = ResponseReceived
	result.UpstreamStatus = resp.StatusCode
	if in.ObserveResponseHeaders != nil {
		in.ObserveResponseHeaders(resp.StatusCode, resp.Header)
	}
	if resp.StatusCode != http.StatusOK {
		body, readErr := readBounded(resp.Body, e.maxResponseBytes)
		if readErr != nil {
			fail("failed reading upstream error", readErr, UpstreamError)
			return
		}
		detail := ErrorDetail{Message: http.StatusText(resp.StatusCode), Type: "upstream_error"}
		var envelope struct {
			Error ErrorDetail `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			detail = envelope.Error
		}
		result.StatusCode = resp.StatusCode
		result.Error = &Error{StatusCode: resp.StatusCode, Error: detail}
		return
	}
	if controller, supported := output.(interface{ SetWriteDeadline(time.Time) error }); supported {
		if deadline, ok := ctx.Deadline(); ok {
			if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				fail("failed setting downstream write deadline", err, ClientWriteError)
				return
			}
			defer controller.SetWriteDeadline(time.Time{})
		}
	}
	outputCall := func(call func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stages.beginOutput()
		defer stages.endOutput()
		if err := ctx.Err(); err != nil {
			return err
		}
		if budget := in.HTTPStageTimeouts.DownstreamWrite; budget > 0 {
			deadline := time.Now().Add(budget)
			outer, bounded := ctx.Deadline()
			if bounded && outer.Before(deadline) {
				deadline = outer
			}
			if err := controller.SetWriteDeadline(deadline); err != nil {
				return err
			}
			defer controller.SetWriteDeadline(outer)
		}
		err := call()
		if err != nil {
			stages.outputFailed(err)
		}
		return err
	}
	write := func(data []byte, stream bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !output.State().Committed {
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
			}
			if err := outputCall(func() error {
				return output.Commit(ctx, ResponseMetadata{StatusCode: http.StatusOK, ContentType: contentType})
			}); err != nil {
				return err
			}
			if !output.State().Committed {
				return errors.New("output did not commit the response")
			}
		}
		var n int
		err := outputCall(func() error { var err error; n, err = output.Write(ctx, data); return err })
		if n < 0 || n > len(data) {
			return errors.New("output returned an invalid write count")
		}
		result.BytesWritten += int64(n)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err == nil && stream {
			err = outputCall(func() error { return output.Flush(ctx) })
		}
		return err
	}
	if !in.Stream {
		body, err := readBounded(resp.Body, e.maxResponseBytes)
		if err != nil {
			fail("failed reading upstream response", err, UpstreamError)
			return
		}
		out, usage, err := in.Decoder.Decode(body)
		result.Usage = usage
		if err != nil {
			fail("failed decoding upstream response", err, UpstreamError)
			return
		}
		result.ProtocolComplete = true
		if err = write(out, false); err != nil {
			fail("failed writing downstream response", err, ClientWriteError)
			return
		}
	} else {
		decoder := in.Decoder.NewStream()
		if decoder == nil {
			fail("missing stream decoder", nil, UpstreamError)
			return
		}
		emit := func(frames []Frame) error {
			for _, frame := range frames {
				if !stages.progress(frame) {
					return ctx.Err()
				}
				if frame.Complete && stages != nil {
					result.ProtocolComplete = true
				}
				if frame.Usage != nil {
					result.Usage = frame.Usage
				}
				if len(frame.Data) > 0 {
					if err := write(frame.Data, true); err != nil {
						return err
					}
				}
			}
			return nil
		}
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), e.maxLineBytes)
		complete := false
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				fail("stream canceled", err, Canceled)
				return
			}
			frames, err := decoder.DecodeLine(scanner.Bytes())
			if err != nil {
				fail("invalid or failed upstream stream", err, UpstreamError)
				return
			}
			if err = emit(frames); err != nil {
				fail("failed writing downstream stream", err, ClientWriteError)
				return
			}
			for _, frame := range frames {
				if frame.Complete {
					complete = true
				}
			}
			if complete {
				break
			}
		}
		if err := scanner.Err(); err != nil {
			fail("failed reading upstream stream", err, UpstreamError)
			return
		}
		frames, err := decoder.End()
		if err != nil {
			fail("upstream stream ended prematurely", err, UpstreamError)
			return
		}
		if !stages.progress(Frame{Complete: true}) {
			fail("stream canceled before protocol completion", ctx.Err(), Canceled)
			return
		}
		result.ProtocolComplete = true
		if err = emit(frames); err != nil {
			fail("failed writing stream terminator", err, ClientWriteError)
			return
		}
	}
	if err := ctx.Err(); err != nil {
		fail("response canceled", err, Canceled)
		return
	}
	result.StatusCode = http.StatusOK
	result.Termination = Success
	return
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}
