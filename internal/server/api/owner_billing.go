package api

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	ledger "minigateway/internal/foundation/contracts/ownerledger/v1"
)

func ownerBillingFailure(c *gin.Context, err error) {
	status, kind, message := 503, "unavailable", "Owner ledger is unavailable."
	switch {
	case errors.Is(err, ledger.ErrInvalid):
		status, kind, message = 400, "invalid_request", "Invalid owner ledger query."
	case errors.Is(err, ledger.ErrUnauthorized):
		status, kind, message = 401, "unauthorized", "Valid login session required."
	case errors.Is(err, ledger.ErrNotFound):
		status, kind, message = 404, "not_found", "Billing reservation not found."
	case errors.Is(err, ledger.ErrRateLimited):
		status, kind, message = 429, "rate_limited", "Owner ledger query rate exceeded."
		c.Header("Retry-After", "60")
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

func parseOwnerBillingQuery(raw string) (ledger.Query, error) {
	query := ledger.Query{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, ledger.ErrInvalid
	}
	for key, entries := range values {
		if len(entries) != 1 || entries[0] == "" {
			return query, ledger.ErrInvalid
		}
		value := entries[0]
		switch key {
		case "state":
			if value != "reserved" && value != "active" && value != "pending" && value != "settled" {
				return query, ledger.ErrInvalid
			}
			query.State = value
		case "token_id":
			n, err := strconv.ParseUint(value, 10, strconv.IntSize-1)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
				return query, ledger.ErrInvalid
			}
			query.TokenID = uint(n)
		case "limit", "offset":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || strconv.Itoa(n) != value {
				return query, ledger.ErrInvalid
			}
			if key == "limit" {
				if n < 1 || n > 100 {
					return query, ledger.ErrInvalid
				}
				query.Limit = n
			} else {
				if n > 10000 {
					return query, ledger.ErrInvalid
				}
				query.Offset = n
			}
		default:
			return query, ledger.ErrInvalid
		}
	}
	return query, nil
}

func (h *Handler) ownerBillingSummary(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		ownerBillingFailure(c, ledger.ErrInvalid)
		return
	}
	if h.ownerLedger == nil {
		ownerBillingFailure(c, ledger.ErrUnavailable)
		return
	}
	out, err := h.ownerLedger.Summary(c.Request.Context(), ledger.Access{Session: bearer(c)})
	if err != nil {
		ownerBillingFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out})
}

func (h *Handler) ownerBillingList(c *gin.Context) {
	query, err := parseOwnerBillingQuery(c.Request.URL.RawQuery)
	if err != nil {
		ownerBillingFailure(c, err)
		return
	}
	if h.ownerLedger == nil {
		ownerBillingFailure(c, ledger.ErrUnavailable)
		return
	}
	out, err := h.ownerLedger.List(c.Request.Context(), ledger.Access{Session: bearer(c)}, query)
	if err != nil {
		ownerBillingFailure(c, err)
		return
	}
	c.JSON(200, out)
}

func (h *Handler) ownerBillingRead(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		ownerBillingFailure(c, ledger.ErrInvalid)
		return
	}
	if h.ownerLedger == nil {
		ownerBillingFailure(c, ledger.ErrUnavailable)
		return
	}
	out, err := h.ownerLedger.Read(c.Request.Context(), ledger.Access{Session: bearer(c)}, c.Param("id"))
	if err != nil {
		ownerBillingFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out})
}
