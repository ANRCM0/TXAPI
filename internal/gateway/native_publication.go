package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"sort"
	"strings"
	"time"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"minigateway/internal/model"
)

type nativeGeneration struct {
	transport NativeWSResource
	channels  map[int]*channel
}

func (g *nativeGeneration) Close(ctx context.Context) error { return g.transport.Close(ctx) }

// Publish is the single management apply owner. Nothing fallible follows the
// existing gateway's rollback-safe apply and the final native pointer swap.
func (s *NativeWebSocketService) Publish(ctx context.Context, channels []model.Channel) error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if err := s.resources.recoverCleanup(ctx); err != nil {
		return err
	}
	if err := s.gateway.validateChannels(channels); err != nil {
		return err
	}
	compiled := &registry{channels: make(map[int]*channel), modelIndex: make(map[string][]int), adaptors: maps.Clone(s.gateway.adaptors)}
	if err := compiled.apply(channels); err != nil {
		return err
	}
	revision, err := nativeRevision(channels)
	if err != nil {
		return err
	}
	s.resources.mu.Lock()
	if s.resources.draining {
		s.resources.mu.Unlock()
		return errors.New("native websocket resources are draining")
	}
	same := s.resources.current != nil && s.resources.current.revision == revision
	if same {
		err := s.gateway.Publish(channels)
		s.resources.mu.Unlock()
		return err
	}
	s.resources.mu.Unlock()
	err = s.resources.prepare(ctx, revision, func(preparing context.Context) (websocketResource, error) {
		transport, err := s.options.Prepare(preparing)
		if nilInterface(transport) {
			return nil, err
		}
		return &nativeGeneration{transport: transport, channels: compiled.channels}, err
	})
	if err != nil {
		return err
	}
	if err = s.resources.publish(revision, func() error { return s.gateway.Publish(channels) }); err != nil {
		s.resources.discard()
	}
	return err
}

func nativeRevision(channels []model.Channel) (string, error) {
	type credential struct {
		ID                uint
		Name, Key, Status string
		Weight            int
		Generation        int64
		Created           time.Time
	}
	type configuration struct {
		ID                             uint
		Name, Type, URL, Group, Status string
		Models                         []string
		Mapping                        map[string]string
		Priority, Weight               int
		Created                        time.Time
		Credentials                    []credential
	}
	values := make([]configuration, 0, len(channels))
	for _, ch := range channels {
		value := configuration{ID: ch.ID, Name: ch.Name, Type: ch.Type, URL: ch.BaseURL, Group: ch.GroupName, Status: ch.Status, Models: append([]string(nil), ch.Models...), Mapping: maps.Clone(map[string]string(ch.ModelMapping)), Priority: ch.Priority, Weight: ch.Weight, Created: ch.CreatedAt}
		for _, c := range ch.Credentials {
			status := c.Status
			if status == model.CredentialCooldown || status == "" {
				status = model.CredentialActive
			}
			value.Credentials = append(value.Credentials, credential{ID: c.ID, Name: c.Name, Key: c.APIKey, Status: status, Weight: c.Weight, Generation: c.HealthGeneration, Created: c.CreatedAt})
		}
		sort.Slice(value.Credentials, func(i, j int) bool { return value.Credentials[i].ID < value.Credentials[j].ID })
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	body, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	defer clear(body)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func officialNativeChannel(ch *channel) bool {
	if ch.typ != "openai" {
		return false
	}
	u, err := url.Parse(ch.baseURL)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, "api.openai.com") && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}

// Configuration comes from the pinned immutable generation. Realtime health
// comes only from the same still-current credential identity/material.
func (s *NativeWebSocketService) nativeCandidates(generation *nativeGeneration, name string) []*channel {
	s.gateway.reg.mu.RLock()
	defer s.gateway.reg.mu.RUnlock()
	var out []*channel
	for id, snapshot := range generation.channels {
		if !snapshot.supports(name) || !officialNativeChannel(snapshot) {
			continue
		}
		actual := name
		if mapped := snapshot.mapping[name]; mapped != "" {
			actual = mapped
		}
		if !s.allowed[actual] {
			continue
		}
		live := s.gateway.reg.channels[id]
		if live == nil || live.typ != snapshot.typ || live.baseURL != snapshot.baseURL || !live.supports(name) {
			continue
		}
		liveActual := name
		if mapped := live.mapping[name]; mapped != "" {
			liveActual = mapped
		}
		if liveActual != actual {
			continue
		}
		copied := *snapshot
		copied.credentials = nil
		for _, pinned := range snapshot.credentials {
			if !isStatic(&pinned) {
				continue
			}
			for _, current := range live.credentials {
				if isStatic(&current) && current.ID == pinned.ID && current.HealthGeneration == pinned.HealthGeneration && current.APIKey == pinned.APIKey && current.CreatedAt.Equal(pinned.CreatedAt) {
					copied.credentials = append(copied.credentials, current)
					break
				}
			}
		}
		out = append(out, &copied)
	}
	return out
}

func (n *nativeSession) targetForRound(ctx context.Context, name string) (*channel, *model.Credential, error) {
	s := n.service
	if n.lease == nil {
		lease, err := s.resources.acquire()
		if err != nil {
			return nil, nil, err
		}
		generation, ok := lease.(*websocketResourceLease).generation.resource.(*nativeGeneration)
		if !ok {
			lease.Release()
			return nil, nil, errors.New("invalid native resource generation")
		}
		n.lease, n.generation = lease, generation
	} else {
		s.resources.mu.Lock()
		current := !s.resources.draining && s.resources.current != nil && s.resources.current.resource == n.generation
		s.resources.mu.Unlock()
		if !current {
			return nil, nil, errors.New("native resource revision changed")
		}
	}
	candidates := s.nativeCandidates(n.generation, name)
	if n.target.ChannelID == 0 {
		ch, cred := s.gateway.selectTargetContext(ctx, candidates, nil, string(n.id), time.Now())
		if ch == nil || cred == nil {
			return nil, nil, errors.New("native target unavailable")
		}
		n.target = responseTarget(ch, cred, name)
		n.credential = *cred
		n.model = name
		return ch, cred, nil
	}
	for _, ch := range candidates {
		for _, cred := range ch.credentials {
			if n.model == name && responseTarget(ch, &cred, name) == n.target && cred.HealthGeneration == n.credential.HealthGeneration && cred.CreatedAt.Equal(n.credential.CreatedAt) && cred.IsUsable(time.Now()) && cred.APIKey != "" {
				copy := cred
				return ch, &copy, nil
			}
		}
	}
	return nil, nil, errors.New("native target binding changed")
}

var _ websocketResource = (*nativeGeneration)(nil)
var _ v2.Lease = (*websocketResourceLease)(nil)
