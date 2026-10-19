package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	eligibility "minigateway/internal/foundation/contracts/upstreamauth/v3"
)

func (h *Handler) eligibilityAvailable(c *gin.Context) bool {
	if h.eligibility != nil {
		return true
	}
	c.JSON(503, gin.H{"error": gin.H{"type": "managed_inference_unavailable", "message": "Managed inference eligibility is unavailable"}})
	return false
}

func eligibilityResult(c *gin.Context, result eligibility.Eligibility, err error) {
	if err == nil {
		c.JSON(200, gin.H{"data": result})
		return
	}
	code, kind, message := 503, "managed_inference_unavailable", "Managed inference eligibility is unavailable"
	switch {
	case errors.Is(err, auth.ErrInvalid):
		code, kind, message = 400, "invalid_request", "Invalid eligibility request"
	case errors.Is(err, managed.ErrNotFound), errors.Is(err, auth.ErrNotFound):
		code, kind, message = 404, "not_found", "Managed credential not found"
	case errors.Is(err, managed.ErrConflict), errors.Is(err, auth.ErrConflict):
		code, kind, message = 409, "eligibility_conflict", "Managed credential eligibility changed; read the current state"
	}
	c.JSON(code, gin.H{"error": gin.H{"type": kind, "message": message}})
}

func eligibilityID(c *gin.Context) (uint, bool) {
	id, ok := parseID(c, "id")
	if !ok {
		return 0, false
	}
	if id == 0 {
		eligibilityResult(c, eligibility.Eligibility{}, auth.ErrInvalid)
		return 0, false
	}
	return id, true
}

func (h *Handler) GetManagedEligibility(c *gin.Context) {
	if !h.eligibilityAvailable(c) {
		return
	}
	id, ok := eligibilityID(c)
	if !ok {
		return
	}
	result, err := h.eligibility.ReadEligibility(c.Request.Context(), id)
	eligibilityResult(c, result, err)
}

func (h *Handler) ResumeManagedEligibility(c *gin.Context) {
	if !h.eligibilityAvailable(c) {
		return
	}
	id, ok := eligibilityID(c)
	if !ok {
		return
	}
	var body struct {
		Binding  string `json:"expected_binding_revision"`
		Revision string `json:"expected_gate_revision"`
		Epoch    string `json:"expected_gate_epoch"`
	}
	if !managedJSON(c, &body) {
		return
	}
	binding, bErr := parseManagedRevision(body.Binding)
	revision, rErr := parseManagedRevision(body.Revision)
	epoch, eErr := parseManagedRevision(body.Epoch)
	if bErr != nil || rErr != nil || eErr != nil {
		eligibilityResult(c, eligibility.Eligibility{}, auth.ErrInvalid)
		return
	}
	result, err := h.eligibility.ResumeEligibility(c.Request.Context(), eligibility.ResumeRequest{CredentialID: id, ExpectedBindingRevision: binding, ExpectedGateRevision: revision, ExpectedGateEpoch: epoch})
	eligibilityResult(c, result, err)
}
