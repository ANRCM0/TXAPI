package openaiauth

import (
	"strings"
	"testing"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func TestSIWCDirectoryOnlyAcceptsVisibleConfiguredSlug(t *testing.T) {
	valid := `{"models":[{"slug":"chosen","visibility":"list","display_name":"Chosen"},{"slug":"hidden","visibility":"hide"}]}`
	if err := ValidateVisibleModel([]byte(valid), "chosen"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"data":[{"id":"chosen"}]}`, `{"models":null}`, `{"models":[]}`,
		`{"models":[{"slug":"chosen","visibility":"hidden"}]}`,
		`{"models":[{"slug":"chosen","visibility":true}]}`,
		`{"models":[{"slug":"chosen","visibility":"list"},{"slug":"chosen","visibility":"hidden"}]}`,
		`{"models":[{"slug":"chosen","slug":"other","visibility":"list"}]}`,
		`{"models":[{"slug":"other","visibility":"list"}]}`,
		valid + `{}`, strings.Repeat(" ", auth.MaxResponseBytes+1),
	} {
		if ValidateVisibleModel([]byte(body), "chosen") == nil {
			t.Fatalf("ambiguous or invisible model accepted: %.200s", body)
		}
	}
}
