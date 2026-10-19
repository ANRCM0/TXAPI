package openaiauth

import (
	"encoding/json"
	"errors"
	"strings"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/protocol/jsonutil"
)

// ValidateVisibleModel decodes the SIWC account catalog. A visible slug is a
// catalog observation, not proof that a subsequent inference will succeed.
func ValidateVisibleModel(body []byte, slug string) error {
	invalid := errors.New("managed model directory is unavailable")
	if len(body) == 0 || len(body) > auth.MaxResponseBytes || !auth.ValidWireText(slug, 128) || strings.TrimSpace(slug) != slug || jsonutil.Validate(body) != nil {
		return invalid
	}
	var catalog struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &catalog) != nil || len(catalog.Models) == 0 || len(catalog.Models) > 1024 {
		return invalid
	}
	seen, visible := make(map[string]bool, len(catalog.Models)), false
	for _, model := range catalog.Models {
		if !auth.ValidWireText(model.Slug, 128) || strings.TrimSpace(model.Slug) != model.Slug || seen[model.Slug] || !auth.ValidWireText(model.Visibility, 64) {
			return invalid
		}
		seen[model.Slug] = true
		if model.Slug == slug && model.Visibility == "list" {
			visible = true
		}
	}
	if !visible {
		return invalid
	}
	return nil
}
