package api

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
)

func usageHistoryFailure(c *gin.Context, err error) {
	status, kind, message := 503, "unavailable", "Usage metadata is unavailable."
	switch {
	case errors.Is(err, history.ErrInvalid):
		status, kind, message = 400, "invalid_request", "Invalid usage metadata query."
	case errors.Is(err, history.ErrUnauthorized):
		status, kind, message = 401, "unauthorized", "Valid login session required."
	case errors.Is(err, history.ErrForbidden):
		status, kind, message = 403, "forbidden", "Usage metadata scope is forbidden."
	case errors.Is(err, history.ErrNotFound):
		status, kind, message = 404, "not_found", "Usage record is not retained."
	case errors.Is(err, history.ErrRateLimited):
		status, kind, message = 429, "rate_limited", "Usage metadata query rate exceeded."
		c.Header("Retry-After", "60")
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

func parseUsageHistoryQuery(raw string, administrator bool) (history.Query, error) {
	query := history.Query{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, history.ErrInvalid
	}
	for key, entries := range values {
		if len(entries) != 1 || entries[0] == "" {
			return query, history.ErrInvalid
		}
		value := entries[0]
		switch key {
		case "state":
			if value != "open" && value != "sealed" && value != "interrupted" {
				return query, history.ErrInvalid
			}
			query.State = value
		case "user_id", "token_id":
			n, err := strconv.ParseUint(value, 10, strconv.IntSize-1)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != value || key == "user_id" && !administrator {
				return query, history.ErrInvalid
			}
			if key == "user_id" {
				query.OwnerID = uint(n)
			} else {
				query.TokenID = uint(n)
			}
		case "limit", "offset":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || strconv.Itoa(n) != value {
				return query, history.ErrInvalid
			}
			if key == "limit" {
				if n < 1 || n > 100 {
					return query, history.ErrInvalid
				}
				query.Limit = n
			} else {
				if n > 10000 {
					return query, history.ErrInvalid
				}
				query.Offset = n
			}
		default:
			return query, history.ErrInvalid
		}
	}
	return query, nil
}

func (h *Handler) usageHistoryList(administrator bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		query, err := parseUsageHistoryQuery(c.Request.URL.RawQuery, administrator)
		if err != nil {
			usageHistoryFailure(c, err)
			return
		}
		if h.usageHistory == nil {
			usageHistoryFailure(c, history.ErrUnavailable)
			return
		}
		out, err := h.usageHistory.List(c.Request.Context(), history.Access{Session: bearer(c), Administrator: administrator}, query)
		if err != nil {
			usageHistoryFailure(c, err)
			return
		}
		c.JSON(200, out)
	}
}

func (h *Handler) usageHistoryRead(administrator bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" {
			usageHistoryFailure(c, history.ErrInvalid)
			return
		}
		if h.usageHistory == nil {
			usageHistoryFailure(c, history.ErrUnavailable)
			return
		}
		out, err := h.usageHistory.Read(c.Request.Context(), history.Access{Session: bearer(c), Administrator: administrator}, c.Param("id"))
		if err != nil {
			usageHistoryFailure(c, err)
			return
		}
		c.JSON(200, gin.H{"data": out})
	}
}
