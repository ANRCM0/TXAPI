package execution

import (
	"context"
	"net/http"
	"time"
)

type Transport string

const (
	HTTP       Transport = "http"
	Background Transport = "background"
	WebSocket  Transport = "websocket"
)

type Requirements struct {
	Operation Operation
	Source    string
	Transport Transport
	Stream    bool
	Tools     bool
}

// Request owns its payload. Every slice returned to a callback is a copy.
type Request interface {
	Operation() Operation
	Body() []byte
	Model() string
	Stream() bool
	Requirements() Requirements
	LegacyMaxTokens() int
	PromptText() string
	AffinityText() string
	CompletionText([]byte) string
}

type OperationRegistration struct {
	ID            Operation
	Version       string
	Parse         func([]byte) (Request, error)
	NonReplayable bool
	OpaqueErrors  bool
	Continuity    bool
	Errors        FailureMessages
}

type FailureMessages struct {
	Upstream     string
	Canceled     string
	Deadline     string
	Output       string
	Generation   string
	StrictDenied string
}

type Parser interface {
	ParseRequest(Operation, []byte) (Request, error)
}

// Target is supplied only after authorization, routing and attempt admission.
// Its credential is codec input, never evidence that a request is authorized.
type Target struct {
	ChannelID         int
	ChannelName       string
	ChannelType       string
	BaseURL           string
	APIKey            string
	OriginModel       string
	ActualModel       string
	IsStream          bool
	Operation         Operation
	ResponseID        string
	CredentialIDValue uint
}

func (t *Target) CredentialID() (uint, bool) {
	return t.CredentialIDValue, t.CredentialIDValue != 0
}

type Frame struct {
	Data     []byte
	Usage    *Usage
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
	Method     string
	URL        string
	Header     http.Header
	Body       []byte
	Stream     bool
	Replayable bool
	Timeout    time.Duration
	Decoder    Decoder
}

type Observation struct {
	Native NativeCompletion
	Usage  *Usage
	Failed bool
}

type Observer interface{ Observation() Observation }

// Adapter callbacks prepare and decode only; the framework owns all I/O and billing.
type Adapter interface {
	Name() string
	Supports(Request) bool
	Prepare(Request, Target) (Input, *Error)
	CheckOutputLimit([]byte, int, string) error
}

type ProtocolRegistration struct {
	ID      string
	Version string
	Adapter Adapter
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type ModelCatalog interface{ Models(context.Context) []Model }
