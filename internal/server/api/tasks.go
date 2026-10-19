package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
)

func taskFailure(c *gin.Context, err error) {
	status, code, message := 503, "unavailable", "Task console is unavailable."
	switch {
	case errors.Is(err, task.ErrUnauthorized):
		status, code, message = 401, "unauthorized", "Valid login session required."
	case errors.Is(err, task.ErrForbidden):
		status, code, message = 403, "forbidden", "Task scope is forbidden."
	case errors.Is(err, task.ErrNotFound):
		status, code, message = 404, "not_found", "Background task not found."
	case errors.Is(err, task.ErrInvalid):
		status, code, message = 400, "invalid_request", "Invalid task query or revision."
	case errors.Is(err, task.ErrConflict):
		status, code, message = 409, "conflict", "Task changed. Refresh before requesting cancellation."
	case errors.Is(err, task.ErrRateLimited):
		status, code, message = 429, "rate_limited", "Task console rate exceeded."
		c.Header("Retry-After", "60")
	}
	c.JSON(status, gin.H{"error": gin.H{"type": code, "message": message}})
}

func (h *Handler) taskList(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.tasks == nil {
			taskFailure(c, task.ErrUnavailable)
			return
		}
		q := task.Query{}
		values, e := url.ParseQuery(c.Request.URL.RawQuery)
		if e != nil {
			taskFailure(c, task.ErrInvalid)
			return
		}
		for key, vs := range values {
			if len(vs) != 1 {
				taskFailure(c, task.ErrInvalid)
				return
			}
			switch key {
			case "stage":
				q.Stage = vs[0]
			case "limit":
				q.Limit, e = strconv.Atoi(vs[0])
				if q.Limit < 1 {
					e = task.ErrInvalid
				}
			case "offset":
				q.Offset, e = strconv.Atoi(vs[0])
			case "user_id", "token_id":
				n, err := strconv.ParseUint(vs[0], 10, strconv.IntSize)
				e = err
				if n == 0 {
					e = task.ErrInvalid
				}
				if key == "user_id" {
					if !admin {
						taskFailure(c, task.ErrInvalid)
						return
					}
					q.OwnerID = uint(n)
				} else {
					q.TokenID = uint(n)
				}
			default:
				taskFailure(c, task.ErrInvalid)
				return
			}
			if e != nil {
				taskFailure(c, task.ErrInvalid)
				return
			}
		}
		out, err := h.tasks.List(c.Request.Context(), task.Access{Session: bearer(c), Administrator: admin}, q)
		if err != nil {
			taskFailure(c, err)
			return
		}
		c.JSON(200, out)
	}
}

func (h *Handler) taskRead(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.tasks == nil {
			taskFailure(c, task.ErrUnavailable)
			return
		}
		if c.Request.URL.RawQuery != "" {
			taskFailure(c, task.ErrInvalid)
			return
		}
		out, err := h.tasks.Read(c.Request.Context(), task.Access{Session: bearer(c), Administrator: admin}, c.Param("id"))
		if err != nil {
			taskFailure(c, err)
			return
		}
		c.JSON(200, gin.H{"data": out})
	}
}

func (h *Handler) taskCancel(c *gin.Context) {
	if h.tasks == nil {
		taskFailure(c, task.ErrUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
	if err != nil || len(body) > 1024 || c.Request.URL.RawQuery != "" || jsonvalue.Validate(body) != nil {
		taskFailure(c, task.ErrInvalid)
		return
	}
	var in struct {
		Revision string `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		taskFailure(c, task.ErrInvalid)
		return
	}
	n, err := strconv.ParseUint(in.Revision, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != in.Revision || n == 0 {
		taskFailure(c, task.ErrInvalid)
		return
	}
	out, err := h.tasks.Cancel(c.Request.Context(), task.Access{Session: bearer(c)}, c.Param("id"), n)
	if err != nil {
		taskFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out})
}
