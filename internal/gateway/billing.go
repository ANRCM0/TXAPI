package gateway

import (
	"context"
	"net/http"
	"reflect"

	"minigateway/internal/billing"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/model"
)

// LegacyBilling is the existing best-effort fee path, not a strict guarantee.
// The provider owns prices and persistence; gateway owns execution/finalization.
type LegacyBilling interface {
	PreRequest(tokenID, userID uint, maxTokens int, model string) (*billing.PreDeduct, error)
	PriceOfSnapshot(*billing.PreDeduct, *model.Usage, string, string, string) (float64, bool)
	Settle(tokenID, userID uint, pre *billing.PreDeduct, actual float64) error
}

// StrictBilling is a separate fail-closed lifecycle. StartPreparedAttempt must
// validate the actual wire target/body/headers and durably occupy its bound
// before dispatch. Unknown execution and failed persistence must retain holds.
// Financial domain types stay authoritative; this is not a second ledger.
// The wire body/header arguments are snapshots, not mutable dispatch buffers.
type StrictBilling interface {
	StrictReady() bool
	Reserve(context.Context, uint, uint, string) (*billing.PreDeduct, error)
	StartPreparedAttempt(context.Context, *billing.PreDeduct, dbmodel.BillingAttempt, billing.ProviderTarget, billing.StrictRequest, string, []byte, http.Header) error
	EndAttempt(context.Context, *billing.PreDeduct, int, int, string, *model.Usage) error
	RecordCostRefusal(context.Context, *billing.PreDeduct, error) error
	Finish(context.Context, *billing.PreDeduct, *model.Usage, string, string, bool) (int64, string, error)
}

func (g *Gateway) strictReady() bool {
	return g.strictBilling != nil && g.strictBilling.StrictReady()
}

// Optional interfaces may receive typed nil pointers from application wiring.
func nilBilling(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
