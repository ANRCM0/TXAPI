package gateway

import (
	dbmodel "minigateway/internal/model"
	"minigateway/internal/scheduler"
	"time"
)

// observeHealth updates an immutable runtime snapshot before persistence. Thus a
// failed credential is excluded by the very next request even if the bounded
// persistence worker is delayed or its queue is full.
func (g *Gateway) observeHealth(from *dbmodel.Credential, status int, message string) *dbmodel.Credential {
	if status != 200 && !scheduler.ShouldPenalizeCredential(status) {
		return from
	}
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()
	for id, ch := range g.reg.channels {
		for i, current := range ch.credentials {
			if current.ID != from.ID || current.HealthGeneration != from.HealthGeneration || current.APIKey != from.APIKey {
				continue
			}
			if current.Status == dbmodel.CredentialDisabled || current.Status == dbmodel.CredentialBanned {
				return nil
			}
			if status == 200 && current.FailCount > from.FailCount {
				return nil
			}
			now := time.Now()
			update := scheduler.NextFailure(&current, status, message, now)
			if status == 200 {
				update = scheduler.NextSuccess(&current, now)
			}
			if !update.Changed(&current) {
				return nil
			}
			next := *ch
			next.credentials = append([]dbmodel.Credential(nil), ch.credentials...)
			c := &next.credentials[i]
			c.Status, c.FailCount, c.CooldownUntil = update.Status, update.FailCount, update.CooldownUntil
			c.UpdatedAt = now
			c.LastError, c.LastUsedAt = update.LastError, update.LastUsedAt
			c.HealthObservedAt = now.UnixNano()
			if c.HealthObservedAt <= current.HealthObservedAt {
				c.HealthObservedAt = current.HealthObservedAt + 1
			}
			c.HealthObserved = true
			g.reg.channels[id] = &next
			observed := *c
			return &observed
		}
	}
	return nil
}
