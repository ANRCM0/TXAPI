package persistence

import (
	"encoding/json"
	"strings"
	"testing"

	"minigateway/internal/model"
)

func TestManagementProjectionsKeepEmptyListsAndHidePersistenceSecrets(t *testing.T) {
	user := UserProjection(model.User{ID: 1, PasswordHash: "fixture-password-digest", Models: model.StringList{}})
	key := TokenProjection(model.Token{ID: 2, KeyHash: "fixture-call-key-digest", Models: model.StringList{}})
	for _, value := range []any{user, key} {
		encoded, err := json.Marshal(value)
		if err != nil || !strings.Contains(string(encoded), `"models":[]`) || strings.Contains(string(encoded), "digest") || strings.Contains(string(encoded), "deleted_at") {
			t.Fatalf("management representation changed or leaked internal state: %s %v", encoded, err)
		}
	}
	if UserProjection(model.User{}).Models != nil || TokenProjection(model.Token{}).Models != nil {
		t.Fatal("nil compatibility lists were changed to non-nil values")
	}
}
