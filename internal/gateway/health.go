package gateway

import (
	dbmodel "minigateway/internal/model"
	"minigateway/internal/scheduler"
	"time"
)

// observeHealth updates an immutable runtime snapshot before persistence. Thus a
// failed credential is excluded by the very next request even if the bounded
// persistence worker is delayed or its queue is full.
func (g *Gateway) observeHealth(from *dbmodel.Credential, status int, message string) {
	if status != 200 && !scheduler.ShouldPenalizeCredential(status) {
		return
	}
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()
	for id, ch := range g.reg.channels {
		for i, current := range ch.credentials {
			if current.ID != from.ID || current.HealthGeneration != from.HealthGeneration || current.APIKey != from.APIKey {
				continue
			}
			if current.Status == dbmodel.CredentialDisabled || current.Status == dbmodel.CredentialBanned {
				return
			}
			if status == 200 && current.FailCount > from.FailCount {
				return
			}
			now := time.Now()
			update := scheduler.NextFailure(&current, status, message, now)
			if status == 200 {
				update = scheduler.NextSuccess(&current, now)
			}
			if !update.Changed(&current) {
				return
			}
			next := *ch
			next.credentials = append([]dbmodel.Credential(nil), ch.credentials...)
			c := &next.credentials[i]
			c.Status, c.FailCount, c.CooldownUntil = update.Status, update.FailCount, update.CooldownUntil
			c.UpdatedAt = now
			g.reg.channels[id] = &next
			return
		}
	}
}
