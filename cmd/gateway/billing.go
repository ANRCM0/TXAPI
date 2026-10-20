package main

import (
	"time"

	"minigateway/internal/config"
)

func strictBillingLease(c *config.Config) time.Duration {
	lease := time.Duration(c.Server.TotalTimeoutSeconds+5) * time.Second
	if c.ResponsesWS.Enabled && lease < 125*time.Second {
		lease = 125 * time.Second
	}
	if c.Responses.Background.Enabled && lease < 305*time.Second {
		lease = 305 * time.Second
	}
	return lease
}
