package store

import (
	"testing"
	"time"

	"minigateway/internal/model"
)

func TestS4KeyMetadataEditPreservesExactExpiryIncludingExpiredKeys(t *testing.T) {
	st := newTestStore(t)
	user, err := st.BootstrapAdmin("expiry-admin", "expiry-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &TokenInput{Name: "expiring", ModelMode: "all", ExpiresInSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	for _, expiry := range []int64{key.ExpiresAt, time.Now().Unix() - 60} {
		if err := st.DB().Model(&model.Token{}).Where("id = ?", key.ID).Update("expires_at", expiry).Error; err != nil {
			t.Fatal(err)
		}
		updated, err := st.UpdateOwnedToken(user.ID, key.ID, &TokenInput{Name: "renamed", ModelMode: "all", PreserveExpiry: true})
		if err != nil {
			t.Fatal(err)
		}
		if updated.ExpiresAt != expiry {
			t.Fatalf("metadata edit changed exact expiry from %d to %d", expiry, updated.ExpiresAt)
		}
	}
}
