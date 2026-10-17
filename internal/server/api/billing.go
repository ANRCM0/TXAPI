package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/billing"
)

func billingFailure(c *gin.Context, err error) {
	status, message, kind := 500, "billing operation failed; retry with the same decision", "billing_error"
	switch {
	case errors.Is(err, billing.ErrStrictUnavailable):
		status, message, kind = 503, "billing ledger is unavailable or read-only", "billing_unavailable"
	case errors.Is(err, billing.ErrLedgerInput):
		status, message, kind = 400, err.Error(), "invalid_request"
	case errors.Is(err, billing.ErrLedgerNotFound):
		status, message, kind = 404, err.Error(), "not_found"
	case errors.Is(err, billing.ErrBillingForbidden):
		status, message, kind = 403, err.Error(), "forbidden"
	case errors.Is(err, billing.ErrNotResolvable), errors.Is(err, billing.ErrCostExceedsHold), errors.Is(err, billing.ErrSettlementConflict):
		status, message, kind = 409, err.Error(), "billing_conflict"
	default:
		slog.Error("billing control operation failed", "err", err)
	}
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": kind}})
}

func (h *Handler) BillingReservations(c *gin.Context) {
	q := billing.LedgerQuery{State: c.DefaultQuery("state", billing.Pending), Limit: 50}
	var err error
	if raw := c.Query("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil {
			billingFailure(c, billing.ErrLedgerInput)
			return
		}
	}
	if raw := c.Query("offset"); raw != "" {
		q.Offset, err = strconv.Atoi(raw)
		if err != nil {
			billingFailure(c, billing.ErrLedgerInput)
			return
		}
	}
	var ok bool
	q.UserID, ok = parseUintQuery(c, "user_id")
	if !ok {
		return
	}
	q.TokenID, ok = parseUintQuery(c, "token_id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	rows, err := h.billing.ListReservations(ctx, q)
	if err != nil {
		billingFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": rows, "unit_scale": strconv.FormatInt(billing.UnitScale, 10), "limit": q.Limit, "offset": q.Offset})
}

func (h *Handler) BillingReservation(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	detail, err := h.billing.ReservationDetail(ctx, c.Param("id"))
	if err != nil {
		billingFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": detail, "unit_scale": strconv.FormatInt(billing.UnitScale, 10)})
}

func (h *Handler) ResolveBillingReservation(c *gin.Context) {
	var in billing.ResolutionInput
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		billingFailure(c, billing.ErrLedgerInput)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		billingFailure(c, billing.ErrLedgerInput)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	result, err := h.billing.ResolveReservation(ctx, c.Param("id"), sessionUser(c).ID, in)
	if err != nil {
		billingFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": result, "unit_scale": strconv.FormatInt(billing.UnitScale, 10)})
}
