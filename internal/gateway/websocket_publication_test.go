package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/config"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/management"
	"minigateway/internal/store"
)

func TestWebSocketManagementSaveAndApplyRetainCompleteOldSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "publish.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g := testCoordinator(t)
	owner := newWebSocketResources()
	revision := "r1"
	failPrepare := false
	var candidate *trackedWebSocketResource
	service := management.NewConfiguration(persistence.Configuration(st), func() error {
		if err := owner.prepare(ctx, revision, func(context.Context) (websocketResource, error) {
			candidate = &trackedWebSocketResource{closed: make(chan struct{})}
			if failPrepare {
				return candidate, errors.New("fixture candidate validation failed")
			}
			return candidate, nil
		}); err != nil {
			return err
		}
		err := owner.publish(revision, func() error { return g.Publish(st.Channels()) })
		if err != nil {
			owner.discard()
		}
		return err
	}, nil)
	original := candidate
	lease, err := owner.acquire()
	if err != nil {
		t.Fatal(err)
	}
	revision = "r2"
	failPrepare = true
	channel, change, err := service.CreateChannel(&cfg.ChannelInput{Name: "native", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"native-model"}, APIKey: "fixture-key", Weight: 1, Status: "enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if channel == nil || !change.Saved || change.Publication.DesiredRevision != 2 || change.Publication.AppliedRevision != 1 || change.Publication.LastError == "" {
		t.Fatalf("save/apply conflated: %+v", change)
	}
	if len(st.Channels()) != 1 || len(g.pickChannels("native-model", nil)) != 0 {
		t.Fatal("failed candidate changed applied routing or lost saved desired")
	}
	resourceWait(t, candidate.closed)
	waitWebSocketOwnerCleanup(t, owner)
	old, err := owner.acquire()
	if err != nil || old.Revision() != "r1" {
		t.Fatal("old runtime not retained")
	}
	_ = old.Release()
	failPrepare = false
	status := service.Apply()
	if status.Channels.DesiredRevision != 2 || status.Channels.AppliedRevision != 2 || status.Channels.LastError != "" || len(g.pickChannels("native-model", nil)) != 1 || len(st.Channels()) != 1 {
		t.Fatalf("retry did not atomically publish saved snapshot: %+v", status)
	}
	select {
	case <-original.closed:
		t.Fatal("old resource closed before final lease")
	default:
	}
	_ = lease.Release()
	resourceWait(t, original.closed)
	if err := owner.drain(ctx); err != nil {
		t.Fatal(err)
	}
}
