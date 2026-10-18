package generation

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
	"math"
	"strconv"
	"time"

	"minigateway/internal/access"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/secrets"
)

type BackgroundOptions struct {
	Repository        bs.Repository
	Parser            execv1.Parser
	BillingMode       string
	ActiveKey         string
	Keys              map[string][]byte
	DigestKey         []byte
	Retention         time.Duration
	MetadataRetention time.Duration
	Limits            bs.Limits
}

// BackgroundService owns authenticated durable acceptance, not network dispatch.
// Assembly supplies the original transaction owner, parser and shared key ring.
type BackgroundService struct {
	repository        bs.Repository
	parser            execv1.Parser
	mode              string
	keys              *secrets.Ring
	digest            []byte
	version           string
	retention         time.Duration
	metadataRetention time.Duration
	limits            bs.Limits
}

type backgroundPayload struct {
	Operation execv1.Operation
	Body      json.RawMessage
}

func NewBackgroundService(ctx context.Context, o BackgroundOptions) (*BackgroundService, error) {
	if ctx == nil || nilPort(o.Repository) || nilPort(o.Parser) || o.BillingMode != "disabled" {
		return nil, bg.ErrUnsupportedBilling
	}
	if len(o.DigestKey) != 32 || o.Retention < 5*time.Minute || o.Retention > time.Hour || o.MetadataRetention < o.Retention || o.MetadataRetention > 7*24*time.Hour ||
		o.Limits.OwnerRecords < 1 || o.Limits.InstanceRecords < o.Limits.OwnerRecords || o.Limits.InstanceRecords > 4096 ||
		o.Limits.OwnerBytes < bs.MaxInput+bs.MaxContent+512 || o.Limits.InstanceBytes < o.Limits.OwnerBytes || o.Limits.InstanceBytes > 1<<30 ||
		o.Limits.OwnerActive < 1 || o.Limits.InstanceActive < o.Limits.OwnerActive || o.Limits.InstanceActive > o.Limits.InstanceRecords || o.Limits.OwnerActive > o.Limits.OwnerRecords {
		return nil, bg.ErrCapacity
	}
	for _, key := range o.Keys {
		if bytes.Equal(key, o.DigestKey) {
			return nil, bg.ErrUnavailable
		}
	}
	ring, err := secrets.NewRing(o.ActiveKey, o.Keys)
	if err != nil {
		return nil, bg.ErrUnavailable
	}
	sum := sha256.Sum256(o.DigestKey)
	s := &BackgroundService{repository: o.Repository, parser: o.Parser, mode: o.BillingMode, keys: ring, digest: bytes.Clone(o.DigestKey), version: hex.EncodeToString(sum[:]), retention: o.Retention, metadataRetention: o.MetadataRetention, limits: o.Limits}
	records, err := o.Repository.InitializeBackground(ctx, s.version, o.BillingMode)
	if err != nil {
		return nil, safeBackgroundError(err)
	}
	// Verify all retained encrypted state before exposing the service. Missing or
	// changed keys must not silently turn a previously accepted task into a retry.
	for _, r := range records {
		if r.DigestVersion != s.version {
			return nil, bg.ErrUnavailable
		}
		if len(r.Ciphertext) > 0 {
			if _, err := s.keys.Open(r.Ciphertext, backgroundAAD(r, "input")); err != nil {
				return nil, bg.ErrUnavailable
			}
		}
		if len(r.Snapshot) > 0 {
			if _, err := s.openSnapshot(r); err != nil {
				return nil, bg.ErrUnavailable
			}
		}
	}
	return s, nil
}

func backgroundAAD(r bs.Record, kind string) []byte {
	data, _ := json.Marshal(struct {
		Domain, Kind, ID, PublicID, Operation, Model, RequestHash, DigestVersion string
		UserID, TokenID                                                          uint
		CreatedAt, Deadline, ExpiresAt                                           int64
	}{
		"TXAPI/background/v1", kind, r.ID, r.PublicID, r.Operation, r.Model, r.RequestHash, r.DigestVersion, r.UserID, r.TokenID, r.CreatedAt, r.Deadline, r.ContentExpiresAt})
	return data
}
func (s *BackgroundService) hash(domain string, b []byte) string {
	h := hmac.New(sha256.New, s.digest)
	_, _ = h.Write([]byte("TXAPI/background/v1/" + domain + "\x00"))
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *BackgroundService) foregroundNamespace(b identity.Binding, key string) string {
	// Match the released Responses receipt key domain without changing the
	// v13 background key digest or retaining the plaintext idempotency key.
	h := hmac.New(sha256.New, s.digest)
	_, _ = h.Write([]byte("TXAPI/responses/key/v1\x00"))
	_, _ = h.Write([]byte(fmt.Sprintf("%d/%d/%s", b.UserID, b.TokenID, key)))
	return hex.EncodeToString(h.Sum(nil))
}
func backgroundRandom() (string, error) {
	var raw [32]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(raw[:]), nil
}
func backgroundBinding(ctx context.Context) (identity.Binding, error) {
	p, ok := access.PrincipalFromContext(ctx)
	if !ok || p.UserID == 0 || p.TokenID == 0 {
		return identity.Binding{}, bg.ErrUnauthorized
	}
	return identity.Binding{UserID: p.UserID, TokenID: p.TokenID}, nil
}
func safeBackgroundError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{bg.ErrUnavailable, bg.ErrUnauthorized, bg.ErrNotFound, bg.ErrConflict, bg.ErrCapacity, bg.ErrUncertain, bg.ErrCursorUnavailable, bg.ErrUnsupportedBilling, bg.ErrUnsupportedCapability, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return bg.ErrUnavailable
}
func canonicalBackground(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > bs.MaxInput || jsonvalue.Validate(body) != nil {
		return nil, bg.ErrConflict
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var fields map[string]json.RawMessage
	if d.Decode(&fields) != nil || fields == nil {
		return nil, bg.ErrConflict
	}
	var background bool
	if json.Unmarshal(fields["background"], &background) != nil || !background {
		return nil, bg.ErrUnsupportedCapability
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if raw := fields[key]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, bg.ErrUnsupportedCapability
		}
	}
	var value interface{}
	d = json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, bg.ErrConflict
	}
	out, e := json.Marshal(value)
	if e != nil {
		return nil, bg.ErrConflict
	}
	return out, nil
}
func (s *BackgroundService) Submit(ctx context.Context, in bg.Submit) (bg.Receipt, error) {
	if s == nil || s.mode != "disabled" {
		return bg.Receipt{}, bg.ErrUnsupportedBilling
	}
	binding, err := backgroundBinding(ctx)
	if err != nil {
		return bg.Receipt{}, err
	}
	if len(in.ID) != 69 || in.ID[:5] != "resp_" {
		return bg.Receipt{}, bg.ErrConflict
	}
	if decoded, e := hex.DecodeString(in.ID[5:]); e != nil || len(decoded) != 32 {
		return bg.Receipt{}, bg.ErrConflict
	}
	if len(in.IdempotencyKey) > 256 {
		return bg.Receipt{}, bg.ErrConflict
	}
	for _, ch := range in.IdempotencyKey {
		if ch < 33 || ch > 126 {
			return bg.Receipt{}, bg.ErrConflict
		}
	}
	body, err := canonicalBackground(in.Body)
	if err != nil {
		return bg.Receipt{}, err
	}
	request, err := s.parser.ParseRequest(in.Operation, bytes.Clone(body))
	if err != nil || nilPort(request) {
		return bg.Receipt{}, bg.ErrUnsupportedCapability
	}
	if request.Operation() != in.Operation || request.Requirements().Operation != in.Operation || request.Requirements().Transport != execv1.Background || request.Stream() != in.Stream || request.Requirements().Stream != in.Stream || !bytes.Equal(request.Body(), body) || request.Model() == "" {
		return bg.Receipt{}, bg.ErrUnsupportedCapability
	}
	var flags map[string]json.RawMessage
	if json.Unmarshal(body, &flags) != nil {
		return bg.Receipt{}, bg.ErrConflict
	}
	stream, store := false, true
	for name, target := range map[string]*bool{"stream": &stream, "store": &store} {
		if raw, ok := flags[name]; ok && (bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil) {
			return bg.Receipt{}, bg.ErrConflict
		}
	}
	if stream != in.Stream || store != in.Store {
		return bg.Receipt{}, bg.ErrConflict
	}
	now := time.Now()
	if in.Deadline.IsZero() {
		in.Deadline = now.Add(5 * time.Minute)
	}
	if !in.Deadline.After(now) || in.Deadline.After(now.Add(5*time.Minute)) {
		return bg.Receipt{}, bg.ErrConflict
	}
	id, err := backgroundRandom()
	if err != nil {
		return bg.Receipt{}, bg.ErrUnavailable
	}
	key := in.IdempotencyKey
	if key == "" {
		key = id
	}
	plain, err := json.Marshal(backgroundPayload{Operation: in.Operation, Body: body})
	if err != nil || len(plain) > bs.MaxInput {
		return bg.Receipt{}, bg.ErrCapacity
	}
	r := bs.Record{ID: id, PublicID: in.ID, UserID: binding.UserID, TokenID: binding.TokenID, Operation: string(in.Operation), Model: request.Model(),
		KeyHash: s.hash("key", []byte(key)), NamespaceHash: s.foregroundNamespace(binding, key), RequestHash: s.hash("request", plain), DigestVersion: s.version, Stage: string(bg.StageQueued), Stream: in.Stream, Store: in.Store,
		Revision: 1, CreatedAt: now.UnixMilli(), Deadline: in.Deadline.UnixMilli(), ContentExpiresAt: now.Add(s.retention).UnixMilli(), ExpiresAt: now.Add(s.metadataRetention).UnixMilli(), ReservedBytes: bs.MaxInput + bs.MaxContent + 512, LastSequence: -1}
	r.Ciphertext, err = s.keys.Seal(plain, backgroundAAD(r, "input"))
	if err != nil {
		return bg.Receipt{}, bg.ErrUnavailable
	}
	out, err := s.repository.AdmitBackground(ctx, r, s.limits)
	if err != nil {
		return bg.Receipt{}, safeBackgroundError(err)
	}
	return s.receipt(out)
}
func (s *BackgroundService) receipt(r bs.Record) (bg.Receipt, error) {
	out := bg.Receipt{ID: r.PublicID, Revision: r.Revision, Stage: bg.Stage(r.Stage), Model: r.Model, CancelRequested: r.CancelRequested, CreatedAt: time.UnixMilli(r.CreatedAt), ContentExpiresAt: time.UnixMilli(r.ContentExpiresAt)}
	if len(r.Snapshot) > 0 && r.ContentExpiresAt > time.Now().UnixMilli() {
		snapshot, e := s.openSnapshot(r)
		if e != nil {
			return bg.Receipt{}, bg.ErrUnavailable
		}
		out.Document = bytes.Clone(snapshot.Document)
	}
	return out, nil
}
func (s *BackgroundService) Read(ctx context.Context, id string) (bg.Receipt, error) {
	b, e := backgroundBinding(ctx)
	if e != nil {
		return bg.Receipt{}, e
	}
	r, e := s.repository.ReadBackground(ctx, b, id)
	if e != nil {
		return bg.Receipt{}, safeBackgroundError(e)
	}
	return s.receipt(r)
}
func (s *BackgroundService) Cancel(ctx context.Context, id string) (bg.Receipt, error) {
	b, e := backgroundBinding(ctx)
	if e != nil {
		return bg.Receipt{}, e
	}
	r, e := s.repository.CancelBackground(ctx, b, id)
	if e != nil {
		return bg.Receipt{}, safeBackgroundError(e)
	}
	return s.receipt(r)
}
func (s *BackgroundService) Events(ctx context.Context, id string, q bg.EventQuery) (bg.EventPage, error) {
	b, e := backgroundBinding(ctx)
	if e != nil {
		return bg.EventPage{}, e
	}
	after := int64(-1)
	if q.After != nil {
		if *q.After > math.MaxInt64 {
			return bg.EventPage{}, bg.ErrCursorUnavailable
		}
		after = int64(*q.After)
	}
	limit := q.Limit
	if limit == 0 {
		limit = 64
	}
	if limit < 1 || limit > 128 {
		return bg.EventPage{}, bg.ErrCursorUnavailable
	}
	r, e := s.repository.ReadBackground(ctx, b, id)
	if e != nil {
		return bg.EventPage{}, safeBackgroundError(e)
	}
	if !r.Stream || r.ContentExpiresAt <= time.Now().UnixMilli() {
		return bg.EventPage{}, bg.ErrCursorUnavailable
	}
	if r.Stage == string(bg.StageUncertain) {
		return bg.EventPage{}, bg.ErrUncertain
	}
	snapshot, e := s.openSnapshot(r)
	if e != nil {
		return bg.EventPage{}, e
	}
	if snapshot.EventUnavailable || after > r.LastSequence || r.Stage == string(bg.StageTerminal) && !snapshot.EventComplete && !r.ReplayPending {
		return bg.EventPage{}, bg.ErrCursorUnavailable
	}
	events, e := s.repository.ReadBackgroundEvents(ctx, b, id, after, limit)
	if e != nil {
		return bg.EventPage{}, safeBackgroundError(e)
	}
	out := bg.EventPage{}
	size := 0
	last := after
	for _, event := range events {
		if event.Sequence != last+1 {
			return bg.EventPage{}, bg.ErrCursorUnavailable
		}
		plain, e := s.keys.Open(event.Ciphertext, backgroundAAD(r, "event/"+strconv.FormatInt(event.Sequence, 10)))
		if e != nil {
			return bg.EventPage{}, bg.ErrUnavailable
		}
		if len(plain) > 64<<10 {
			return bg.EventPage{}, bg.ErrCursorUnavailable
		}
		if size+len(plain) > 64<<10 {
			break
		}
		size += len(plain)
		n := uint64(event.Sequence)
		out.Events = append(out.Events, bg.Event{Sequence: n, Data: bytes.Clone(plain)})
		out.Next = &n
		last = event.Sequence
	}
	if len(events) == 0 && after < r.LastSequence {
		return bg.EventPage{}, bg.ErrCursorUnavailable
	}
	out.Terminal = snapshot.EventComplete && last == r.LastSequence
	return out, nil
}

var _ bg.Service = (*BackgroundService)(nil)

func (s *BackgroundService) Kind(ctx context.Context, id string) (bg.ResourceKind, error) {
	b, e := backgroundBinding(ctx)
	if e != nil {
		return "", e
	}
	kind, e := s.repository.BackgroundKind(ctx, b, id)
	return kind, safeBackgroundError(e)
}

func (s *BackgroundService) Delete(ctx context.Context, id string, revision *uint64) error {
	b, e := backgroundBinding(ctx)
	if e != nil {
		return e
	}
	return safeBackgroundError(s.repository.DeleteBackground(ctx, b, id, revision))
}

var _ bg.Resources = (*BackgroundService)(nil)
