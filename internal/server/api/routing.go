package api

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	routes "minigateway/internal/foundation/contracts/routes/v1"
)

func routeFailure(c *gin.Context, err error) {
	status, code, message := 503, "unavailable", "Routing diagnostics are unavailable."
	switch {
	case errors.Is(err, routes.ErrUnauthorized):
		status, code, message = 401, "unauthorized", "Valid login session required."
	case errors.Is(err, routes.ErrForbidden):
		status, code, message = 403, "forbidden", "Routing scope is forbidden."
	case errors.Is(err, routes.ErrInvalid):
		status, code, message = 400, "invalid_request", "Invalid routing query."
	case errors.Is(err, routes.ErrNotFound):
		status, code, message = 404, "not_found", "Routing observation is not retained."
	case errors.Is(err, routes.ErrRateLimited):
		status, code, message = 429, "rate_limited", "Routing console rate exceeded."
		c.Header("Retry-After", "60")
	}
	c.JSON(status, gin.H{"error": gin.H{"type": code, "message": message}})
}

func (h *Handler) routeList(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.routes == nil {
			routeFailure(c, routes.ErrUnavailable)
			return
		}
		query := routes.Query{}
		values, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			routeFailure(c, routes.ErrInvalid)
			return
		}
		for key, values := range values {
			if len(values) != 1 {
				routeFailure(c, routes.ErrInvalid)
				return
			}
			value := values[0]
			switch key {
			case "request_id":
				query.RequestID = value
				if value == "" {
					err = routes.ErrInvalid
				}
			case "limit":
				query.Limit, err = strconv.Atoi(value)
				if query.Limit < 1 {
					err = routes.ErrInvalid
				}
			case "offset":
				query.Offset, err = strconv.Atoi(value)
			case "user_id", "token_id":
				var n uint64
				n, err = strconv.ParseUint(value, 10, strconv.IntSize)
				if n == 0 {
					err = routes.ErrInvalid
				}
				if key == "user_id" {
					if !admin {
						routeFailure(c, routes.ErrInvalid)
						return
					}
					query.OwnerID = uint(n)
				} else {
					query.TokenID = uint(n)
				}
			default:
				err = routes.ErrInvalid
			}
			if err != nil {
				routeFailure(c, routes.ErrInvalid)
				return
			}
		}
		out, err := h.routes.List(c.Request.Context(), routes.Access{Session: bearer(c), Administrator: admin}, query)
		if err != nil {
			routeFailure(c, err)
			return
		}
		c.JSON(200, out)
	}
}

func (h *Handler) routeRead(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.routes == nil {
			routeFailure(c, routes.ErrUnavailable)
			return
		}
		if c.Request.URL.RawQuery != "" {
			routeFailure(c, routes.ErrInvalid)
			return
		}
		out, err := h.routes.Read(c.Request.Context(), routes.Access{Session: bearer(c), Administrator: admin}, c.Param("id"))
		if err != nil {
			routeFailure(c, err)
			return
		}
		c.JSON(200, gin.H{"data": out})
	}
}
