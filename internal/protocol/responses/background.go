package responses

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	bgv2 "minigateway/internal/foundation/contracts/background/v2"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	usagev3 "minigateway/internal/foundation/contracts/execution/v3"
)

const BackgroundRegistrationID = "openai-native-background"

type BackgroundParser struct{}

type backgroundRequest struct {
	native Request
	body   []byte
}

func (r backgroundRequest) Operation() execution.Operation { return execution.Responses }
func (r backgroundRequest) Body() []byte                   { return bytes.Clone(r.body) }
func (r backgroundRequest) Model() string                  { return r.native.Model }
func (r backgroundRequest) Stream() bool                   { return r.native.Stream }
func (r backgroundRequest) Requirements() execution.Requirements {
	return execution.Requirements{Operation: execution.Responses, Source: "openai", Transport: execution.Background, Stream: r.native.Stream}
}
func (r backgroundRequest) LegacyMaxTokens() int         { return r.native.MaxOutputTokens() }
func (r backgroundRequest) PromptText() string           { return r.native.PromptText() }
func (r backgroundRequest) AffinityText() string         { return r.native.PromptText() }
func (r backgroundRequest) CompletionText([]byte) string { return "" }

func (BackgroundParser) ParseRequest(op execution.Operation, body []byte) (execution.Request, error) {
	r, e := ParseRequest(body)
	if e != nil || op != execution.Responses || !r.Background || r.PreviousResponseID != "" || present(r.Conversation) {
		return nil, errors.New("unsupported native background request")
	}
	check := r
	check.Background = false
	if e = check.validateHTTP(false); e != nil {
		return nil, e
	}
	return backgroundRequest{native: r, body: bytes.Clone(body)}, nil
}

type BackgroundCodec struct{}

func BackgroundRegistration() bg.Registration {
	return bg.Registration{ID: BackgroundRegistrationID, Version: bg.Version, Operation: execution.Responses, Parse: BackgroundParser{}, Codec: BackgroundCodec{}}
}

func backgroundFailure() *execution.Error {
	return &execution.Error{StatusCode: 400, Error: execution.ErrorDetail{Message: "native background capability unavailable", Type: "unsupported_background"}}
}

func (BackgroundCodec) Prepare(command bg.RemoteCommand, target execution.Target) (bg.Prepared, *execution.Error) {
	if target.ChannelType != "openai" || target.Operation != execution.Responses || target.ResponseID == "" || target.APIKey == "" || target.ActualModel == "" {
		return bg.Prepared{}, backgroundFailure()
	}
	u, e := url.Parse(strings.TrimRight(target.BaseURL, "/") + "/v1/responses")
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
		return bg.Prepared{}, backgroundFailure()
	}
	method := http.MethodGet
	stream := false
	var body []byte
	switch command.Action {
	case bg.RemoteSubmit:
		if command.RemoteID != "" || command.After != nil {
			return bg.Prepared{}, backgroundFailure()
		}
		r, e := (BackgroundParser{}).ParseRequest(execution.Responses, command.Body)
		if e != nil {
			return bg.Prepared{}, backgroundFailure()
		}
		body, e = r.(backgroundRequest).native.WithModel(target.ActualModel)
		if e != nil {
			return bg.Prepared{}, backgroundFailure()
		}
		method = http.MethodPost
		stream = r.Stream()
	case bg.RemoteInspect, bg.RemoteCancel:
		if !validRemoteID(command.RemoteID) || len(command.Body) != 0 || command.After != nil {
			return bg.Prepared{}, backgroundFailure()
		}
		u.Path += "/" + command.RemoteID
		if command.Action == bg.RemoteCancel {
			u.Path += "/cancel"
			method = http.MethodPost
		}
	case bg.RemoteResume:
		if !validRemoteID(command.RemoteID) || len(command.Body) != 0 || command.After != nil && *command.After > math.MaxInt64 {
			return bg.Prepared{}, backgroundFailure()
		}
		u.Path += "/" + command.RemoteID
		query := url.Values{"stream": {"true"}}
		if command.After != nil {
			query.Set("starting_after", strconv.FormatUint(*command.After, 10))
		}
		u.RawQuery = query.Encode()
		stream = true
	default:
		return bg.Prepared{}, backgroundFailure()
	}
	d := &backgroundDecoder{publicID: target.ResponseID, remoteID: command.RemoteID}
	return bg.Prepared{Input: execution.Input{Method: method, URL: u.String(), Header: http.Header{"Authorization": {"Bearer " + target.APIKey}, "Content-Type": {"application/json"}}, Body: body, Stream: stream, Replayable: false, Timeout: 10 * time.Second, Decoder: d}, Progress: d}, nil
}

func validRemoteID(id string) bool {
	if len(id) < 1 || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r != '_' && r != '-' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type backgroundDecoder struct {
	publicID, remoteID string
	mu                 sync.Mutex
	observation        bg.Observation
	detailedUsage      *usagev3.UsageObservation
}

func (d *backgroundDecoder) Decode(body []byte) ([]byte, *execution.Usage, error) {
	if len(body) > 1<<20 {
		return nil, nil, errors.New("background response too large")
	}
	r, usage, details, e := parseResponse(body)
	if e != nil || !validRemoteID(r.ID) || d.remoteID != "" && r.ID != d.remoteID {
		return nil, nil, errors.New("invalid native background response")
	}
	base, e := usageObservation(r.Usage)
	if e != nil {
		return nil, nil, e
	}
	enriched, e := enrichedUsageObservation(details, base)
	if e != nil {
		return nil, nil, e
	}
	states := map[string]bg.RemoteState{"queued": bg.RemoteAccepted, "in_progress": bg.RemoteRunning, "completed": bg.RemoteSucceeded, "incomplete": bg.RemoteIncomplete, "failed": bg.RemoteFailed, "cancelled": bg.RemoteCancelled}
	out, e := rewrite(body, d.publicID, true, false)
	if e != nil {
		return nil, nil, e
	}
	d.mu.Lock()
	d.observation = bg.Observation{RemoteID: r.ID, State: states[r.Status], Document: bytes.Clone(out), Usage: usage}
	d.detailedUsage = enriched
	d.mu.Unlock()
	return out, usage, nil
}
func (d *backgroundDecoder) NewStream() execution.StreamDecoder {
	return &backgroundStreamDecoder{owner: d, remoteID: d.remoteID, lastSequence: -1}
}
func (d *backgroundDecoder) Drain() bg.Observation {
	return d.DrainUsage().Observation
}
func (d *backgroundDecoder) DrainUsage() bgv2.Observation {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.observation
	u := d.detailedUsage
	d.observation = bg.Observation{}
	d.detailedUsage = nil
	o.Document = bytes.Clone(o.Document)
	if o.Usage != nil {
		u := *o.Usage
		o.Usage = &u
	}
	return bgv2.Observation{Observation: o, Usage: u}
}

var _ execution.Parser = BackgroundParser{}
var _ bg.Codec = BackgroundCodec{}
var _ bgv2.Observer = (*backgroundDecoder)(nil)
