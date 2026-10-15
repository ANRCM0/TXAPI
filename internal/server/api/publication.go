package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/store"
)

// Revisions describe this process, not durable database or schema versions.
type PublicationStatus struct {
	DesiredRevision uint64    `json:"desired_revision"`
	AppliedRevision uint64    `json:"applied_revision"`
	LastError       string    `json:"last_error"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ConfigStatus struct {
	Epoch    string            `json:"epoch"`
	Channels PublicationStatus `json:"channels"`
	Prices   PublicationStatus `json:"prices"`
}

type publication struct {
	mu     sync.Mutex
	status PublicationStatus
	apply  func() error
}

func (p *publication) publish(changed bool) PublicationStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if changed {
		p.status.DesiredRevision++
	}
	p.status.UpdatedAt = time.Now().UTC()
	if p.apply != nil {
		if err := p.apply(); err != nil {
			// Loader errors can contain URLs or database paths; never expose them.
			p.status.LastError = "Publication failed. Previous snapshot retained; check server logs and retry."
			var public interface{ PublicReason() string }
			if errors.As(err, &public) {
				p.status.LastError = public.PublicReason() + ". Previous snapshot retained."
			}
			slog.Error("configuration publication failed", "revision", p.status.DesiredRevision, "err", err)
			return p.status
		}
	}
	p.status.AppliedRevision = p.status.DesiredRevision
	p.status.LastError = ""
	return p.status
}

func (p *publication) snapshot() PublicationStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Serialize control-plane saves with publication and explicit retries. Health
// refreshes use a separate, health-only gateway entry point.
func (h *Handler) configurationWrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.configurationMu.Lock()
		defer h.configurationMu.Unlock()
		c.Next()
	}
}

func publicationHeaders(c *gin.Context, status PublicationStatus) {
	c.Header("X-TXAPI-Desired-Revision", strconv.FormatUint(status.DesiredRevision, 10))
	c.Header("X-TXAPI-Applied-Revision", strconv.FormatUint(status.AppliedRevision, 10))
	state := "applied"
	if status.LastError != "" || status.DesiredRevision != status.AppliedRevision {
		state = "pending"
	}
	c.Header("X-TXAPI-Publication-State", state)
}

func (h *Handler) failConfiguration(c *gin.Context, err error) {
	var saved *store.SavedConfigurationError
	if errors.As(err, &saved) {
		h.notifyChanged(c)
		c.JSON(http.StatusInternalServerError, gin.H{"saved": true, "error": gin.H{
			"type":    "saved_configuration_error",
			"message": "Configuration was saved, but management cache refresh failed. Inspect publication status before retrying the save.",
		}})
		return
	}
	fail(c, err)
}

func (h *Handler) configStatus() ConfigStatus {
	return ConfigStatus{Epoch: h.epoch, Channels: h.channels.snapshot(), Prices: h.prices.snapshot()}
}

func (h *Handler) ConfigurationStatus(c *gin.Context) {
	h.configurationMu.Lock()
	defer h.configurationMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"data": h.configStatus()})
}

func (h *Handler) ApplyConfiguration(c *gin.Context) {
	h.channels.publish(false)
	h.prices.publish(false)
	status := h.configStatus()
	code := http.StatusOK
	if status.Channels.LastError != "" || status.Prices.LastError != "" {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{"data": status})
}
