package gateway

import (
	"fmt"
	"net/url"

	"minigateway/internal/model"
)

type configurationError string

func (e configurationError) Error() string        { return string(e) }
func (e configurationError) PublicReason() string { return string(e) }

// Publish validates the complete candidate before replacing the running pool.
// Unlike legacy startup loading it never silently drops an enabled channel.
func (g *Gateway) Publish(channels []model.Channel) error {
	if err := g.validateChannels(channels); err != nil {
		return err
	}
	return g.Reload(channels)
}

func (g *Gateway) validateChannels(channels []model.Channel) error {
	for _, ch := range channels {
		if err := validateCredentialSources(ch); err != nil {
			return err
		}
		if !ch.IsEnabled() {
			continue
		}
		if _, ok := g.reg.getAdaptor(ch.Type); !ok {
			return configurationError(fmt.Sprintf("Channel %d has an unregistered protocol", ch.ID))
		}
		u, err := url.Parse(ch.BaseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return configurationError(fmt.Sprintf("Channel %d needs an http(s) URL without userinfo or fragment", ch.ID))
		}
	}
	return nil
}

// RefreshHealth cannot publish pending channel/key edits as a side effect of
// background recovery. Only newer observations of the same credential
// generation and key may update the currently applied configuration.
func (g *Gateway) RefreshHealth(channels []model.Channel) {
	incoming := make(map[uint]model.Credential)
	for _, ch := range channels {
		for _, cred := range ch.Credentials {
			incoming[cred.ID] = cred
		}
	}
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()
	for id, ch := range g.reg.channels {
		next := *ch
		next.credentials = append([]model.Credential(nil), ch.credentials...)
		for i, current := range ch.credentials {
			if !isStatic(&current) {
				continue
			}
			update, ok := incoming[current.ID]
			if !ok || update.HealthGeneration != current.HealthGeneration || update.APIKey != current.APIKey || update.HealthObservedAt < current.HealthObservedAt {
				continue
			}
			// At equal observation order, allow expiry recovery, not rollback
			// of an in-memory failure that has not reached the database.
			if update.HealthObservedAt == current.HealthObservedAt && update.Status != model.CredentialActive {
				continue
			}
			c := &next.credentials[i]
			c.Status, c.FailCount, c.CooldownUntil = update.Status, update.FailCount, update.CooldownUntil
			c.LastError, c.LastUsedAt = update.LastError, update.LastUsedAt
			c.HealthObservedAt, c.UpdatedAt = update.HealthObservedAt, update.UpdatedAt
		}
		g.reg.channels[id] = &next
	}
}
