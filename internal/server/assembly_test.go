package server

import (
	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	authentication "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/gateway"
	"minigateway/internal/management"
	"minigateway/internal/metrics"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
	"net/http"
)

type fixtureDeps struct {
	Gateway           *gateway.Gateway
	Cfg               *config.Config
	Store             *store.Store
	Billing           *billing.Engine
	Responses         http.Handler
	AdminAuth         gin.HandlerFunc
	RateLimiter       middleware.Limiter
	IPRPMLimit        int
	TrustedProxyCIDRs []string
}

func fixtureEngine(old fixtureDeps) *gin.Engine {
	configuration := management.NewConfiguration(persistence.Configuration(old.Store), func() error {
		channels, err := old.Store.ChannelSnapshot()
		if err != nil {
			return err
		}
		return old.Gateway.Publish(channels)
	}, func() error {
		if old.Billing != nil && old.Billing.Prices() != nil {
			return old.Billing.Prices().ReloadPrices()
		}
		return nil
	})
	if old.Gateway == nil {
		panic("gateway must be explicitly supplied")
	}
	old.Gateway.Metrics().SetCredentialStatsProvider(old.Gateway.CredentialStats)
	old.Gateway.Metrics().SetLogStatsProvider(func() metrics.LogQueueStats {
		s := old.Store.LogQueueStats()
		return metrics.LogQueueStats{Depth: s.Depth, Capacity: s.Capacity, Dropped: s.Dropped, Failed: s.Failed}
	})
	return NewEngine(Deps{Generation: gateway.NewHTTPExecutor(old.Gateway), TokenAuth: authentication.NewTokenAuthenticator(old.Store), Control: controlplane.New(persistence.Control(old.Store)), Configuration: configuration, Ledger: old.Billing, Metrics: old.Gateway.Metrics(), Responses: old.Responses, AdminAuth: old.AdminAuth, RateLimiter: old.RateLimiter, RateGate: access.NewRateGate(old.RateLimiter), IPRPMLimit: old.IPRPMLimit, TrustedProxyCIDRs: old.TrustedProxyCIDRs})
}
