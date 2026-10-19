package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	jsonvalue "minigateway/internal/foundation/contracts/json/v1"
	quotation "minigateway/internal/foundation/contracts/quotation/v1"
)

type quotationUsageInput struct {
	Input        *string         `json:"input_tokens"`
	Output       *string         `json:"output_tokens"`
	Total        *string         `json:"total_tokens"`
	Cached       *string         `json:"cached_input_tokens"`
	Written      *string         `json:"cache_write_input_tokens"`
	Reasoning    *string         `json:"reasoning_output_tokens"`
	Completeness v2.Completeness `json:"completeness"`
	Source       v2.UsageSource  `json:"source"`
	Profile      string          `json:"profile"`
}

type quotationRequest struct {
	PriceVersion string              `json:"price_version"`
	Model        string              `json:"model"`
	Group        string              `json:"group"`
	Usage        quotationUsageInput `json:"usage"`
}

func quotationFailure(c *gin.Context, err error) {
	status, code, message := 503, "unavailable", "Selling quotations are unavailable."
	switch {
	case errors.Is(err, quotation.ErrUnauthorized):
		status, code, message = 401, "unauthorized", "Valid login session required."
	case errors.Is(err, quotation.ErrForbidden):
		status, code, message = 403, "forbidden", "Administrator required."
	case errors.Is(err, quotation.ErrInvalid):
		status, code, message = 400, "invalid_request", "Invalid selling quotation input."
	case errors.Is(err, quotation.ErrOverflow):
		status, code, message = 400, "quotation_overflow", "Selling quotation amount exceeds the supported range."
	case errors.Is(err, quotation.ErrNotFound):
		status, code, message = 404, "not_found", "Selling quotation version is not available."
	case errors.Is(err, quotation.ErrRateLimited):
		status, code, message = 429, "rate_limited", "Selling quotation rate exceeded."
		c.Header("Retry-After", "60")
	}
	c.JSON(status, gin.H{"error": gin.H{"type": code, "message": message}})
}

func (h *Handler) QuotationCurrent(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	if h.quotation == nil {
		quotationFailure(c, quotation.ErrUnavailable)
		return
	}
	out, err := h.quotation.Current(c.Request.Context(), quotation.Access{Session: bearer(c)})
	if err != nil {
		quotationFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out})
}

func parseQuotationUsage(input quotationUsageInput) (v3.UsageObservation, error) {
	u := v3.UsageObservation{Completeness: input.Completeness, Source: input.Source, Profile: input.Profile}
	fields := []**int64{&u.Counts.InputTokens, &u.Counts.OutputTokens, &u.Counts.TotalTokens,
		&u.Breakdown.CachedInputTokens, &u.Breakdown.CacheWriteInputTokens, &u.Breakdown.ReasoningOutputTokens}
	for i, value := range []*string{input.Input, input.Output, input.Total, input.Cached, input.Written, input.Reasoning} {
		if value == nil {
			continue
		}
		n, err := strconv.ParseInt(*value, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != *value {
			return v3.UsageObservation{}, quotation.ErrInvalid
		}
		*fields[i] = &n
	}
	if err := u.Validate(); err != nil {
		return v3.UsageObservation{}, quotation.ErrInvalid
	}
	return u, nil
}

func (h *Handler) QuotationCalculate(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	if h.quotation == nil {
		quotationFailure(c, quotation.ErrUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024))
	if err != nil || jsonvalue.Validate(body) != nil {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	var in quotationRequest
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	for key := range fields {
		if key != "price_version" && key != "model" && key != "group" && key != "usage" {
			quotationFailure(c, quotation.ErrInvalid)
			return
		}
	}
	var usageFields map[string]json.RawMessage
	if json.Unmarshal(fields["usage"], &usageFields) != nil || usageFields == nil {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	for key := range usageFields {
		switch key {
		case "input_tokens", "output_tokens", "total_tokens", "cached_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens", "completeness", "source", "profile":
		default:
			quotationFailure(c, quotation.ErrInvalid)
			return
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		quotationFailure(c, quotation.ErrInvalid)
		return
	}
	usage, err := parseQuotationUsage(in.Usage)
	if err != nil {
		quotationFailure(c, err)
		return
	}
	out, err := h.quotation.Calculate(c.Request.Context(), quotation.Access{Session: bearer(c)}, quotation.Input{
		PriceVersion: in.PriceVersion, Model: in.Model, Group: in.Group, Usage: usage})
	if err != nil {
		quotationFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out})
}
