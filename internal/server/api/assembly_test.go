package api

import (
	"minigateway/internal/billing"
	"minigateway/internal/controlplane"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/management"
	"minigateway/internal/store"
)

func fixtureHandler(st *store.Store, channels, prices func() error) *Handler {
	return New(Dependencies{Control: controlplane.New(persistence.Control(st)), Configuration: management.NewConfiguration(persistence.Configuration(st), channels, prices), Ledger: (*billing.Engine)(nil)})
}
