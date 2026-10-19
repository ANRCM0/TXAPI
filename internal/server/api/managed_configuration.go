package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func managedJSON(c *gin.Context, dst any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"type": "invalid_request", "message": "Invalid managed configuration body"}})
		return false
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		c.JSON(400, gin.H{"error": gin.H{"type": "invalid_request", "message": "Expected one configuration object"}})
		return false
	}
	return true
}

func (h *Handler) managedAvailable(c *gin.Context) bool {
	if h.managed != nil {
		return true
	}
	c.JSON(503, gin.H{"error": gin.H{"type": "managed_inference_unavailable", "message": "Managed inference configuration is disabled"}})
	return false
}

func managedResult(c *gin.Context, change cfg.Change, err error) bool {
	if change.Saved || err == nil {
		return configurationResult(c, change, err)
	}
	switch {
	case errors.Is(err, auth.ErrInvalid):
		c.JSON(400, gin.H{"error": gin.H{"type": "invalid_request", "message": "Invalid managed configuration"}})
	case errors.Is(err, managed.ErrNotFound):
		c.JSON(404, gin.H{"error": gin.H{"type": "not_found", "message": "Managed credential not found"}})
	case errors.Is(err, managed.ErrConflict):
		c.JSON(409, gin.H{"error": gin.H{"type": "configuration_conflict", "message": "Managed credential configuration changed or conflicts"}})
	default:
		c.JSON(503, gin.H{"error": gin.H{"type": "managed_inference_unavailable", "message": "Managed inference configuration is unavailable"}})
	}
	return false
}

func (h *Handler) CreateManagedChannel(c *gin.Context) {
	if !h.managedAvailable(c) {
		return
	}
	// api_key is deliberately absent, even an explicitly empty field is rejected.
	var input struct {
		Channel struct {
			Name         string            `json:"name"`
			Type         string            `json:"type"`
			BaseURL      string            `json:"base_url"`
			Models       []string          `json:"models"`
			ModelMapping map[string]string `json:"model_mapping"`
			GroupName    string            `json:"group_name"`
			Priority     int               `json:"priority"`
			Weight       int               `json:"weight"`
			Status       string            `json:"status"`
		} `json:"channel"`
		Binding managed.ManagedBindingInput `json:"binding"`
	}
	if !managedJSON(c, &input) {
		return
	}
	in := managed.ManagedChannelInput{Channel: cfg.ChannelInput{
		Name: input.Channel.Name, Type: input.Channel.Type, BaseURL: input.Channel.BaseURL,
		Models: input.Channel.Models, ModelMapping: input.Channel.ModelMapping,
		GroupName: input.Channel.GroupName, Priority: input.Channel.Priority,
		Weight: input.Channel.Weight, Status: input.Channel.Status,
	}, Binding: input.Binding}
	result, change, err := h.managed.CreateManagedChannel(c.Request.Context(), &in)
	if managedResult(c, change, err) {
		c.JSON(http.StatusCreated, gin.H{"data": result})
	}
}

func (h *Handler) GetManagedCredential(c *gin.Context) {
	if !h.managedAvailable(c) {
		return
	}
	id, ok := parseID(c, "id")
	if !ok || id == 0 {
		if ok {
			fail(c, errors.New("invalid credential ID"))
		}
		return
	}
	result, err := h.managed.GetManagedCredential(c.Request.Context(), id)
	if managedResult(c, cfg.Change{}, err) {
		c.JSON(200, gin.H{"data": result})
	}
}

func parseManagedRevision(raw string) (int64, error) {
	if len(raw) == 0 || raw[0] == '0' {
		return 0, errors.New("expected_revision must be a positive decimal string")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("expected_revision must be a positive decimal string")
		}
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 {
		return 0, errors.New("expected_revision must be a positive decimal string")
	}
	return revision, nil
}

func (h *Handler) UpdateManagedCredential(c *gin.Context) {
	if !h.managedAvailable(c) {
		return
	}
	id, ok := parseID(c, "id")
	if !ok || id == 0 {
		if ok {
			fail(c, errors.New("invalid credential ID"))
		}
		return
	}
	var in struct {
		ExpectedRevision string                      `json:"expected_revision"`
		Binding          managed.ManagedBindingInput `json:"binding"`
	}
	if !managedJSON(c, &in) {
		return
	}
	revision, err := parseManagedRevision(in.ExpectedRevision)
	if err != nil {
		fail(c, err)
		return
	}
	result, change, err := h.managed.UpdateManagedCredential(c.Request.Context(), id, revision, &in.Binding)
	if managedResult(c, change, err) {
		c.JSON(200, gin.H{"data": result})
	}
}

func (h *Handler) DisableManagedCredential(c *gin.Context) {
	if !h.managedAvailable(c) {
		return
	}
	id, ok := parseID(c, "id")
	if !ok || id == 0 {
		if ok {
			fail(c, errors.New("invalid credential ID"))
		}
		return
	}
	var in struct {
		ExpectedRevision string `json:"expected_revision"`
	}
	if !managedJSON(c, &in) {
		return
	}
	revision, err := parseManagedRevision(in.ExpectedRevision)
	if err != nil {
		fail(c, err)
		return
	}
	result, change, err := h.managed.DisableManagedCredential(c.Request.Context(), id, revision)
	if managedResult(c, change, err) {
		c.JSON(200, gin.H{"data": result})
	}
}
