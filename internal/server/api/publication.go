package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
)

// Preserve the HTTP DTO names while the application service owns their state.
type PublicationStatus = cfg.PublicationStatus
type ConfigStatus = cfg.ConfigStatus

func publicationHeaders(c *gin.Context, status PublicationStatus) {
	c.Header("X-TXAPI-Desired-Revision", strconv.FormatUint(status.DesiredRevision, 10))
	c.Header("X-TXAPI-Applied-Revision", strconv.FormatUint(status.AppliedRevision, 10))
	state := "applied"
	if status.LastError != "" || status.DesiredRevision != status.AppliedRevision {
		state = "pending"
	}
	c.Header("X-TXAPI-Publication-State", state)
}

func configurationResult(c *gin.Context, change cfg.Change, err error) bool {
	if change.Saved {
		publicationHeaders(c, change.Publication)
	}
	if err == nil {
		return true
	}
	if change.Saved {
		c.JSON(http.StatusInternalServerError, gin.H{"saved": true, "error": gin.H{
			"type":    "saved_configuration_error",
			"message": "Configuration was saved, but management cache refresh failed. Inspect publication status before retrying the save.",
		}})
		return false
	}
	fail(c, err)
	return false
}

func (h *Handler) configStatus() ConfigStatus { return h.configuration.Status() }

func (h *Handler) ConfigurationStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.configStatus()})
}

func (h *Handler) ApplyConfiguration(c *gin.Context) {
	status := h.configuration.Apply()
	code := http.StatusOK
	if status.Channels.LastError != "" || status.Prices.LastError != "" {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{"data": status})
}
