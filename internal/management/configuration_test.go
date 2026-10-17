package management

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func configurationTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "configuration.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func configurationChannel(name string) *store.ChannelInput {
	return &store.ChannelInput{Name: name, Type: "openai", BaseURL: "http://127.0.0.1:9911", Models: []string{"m1"}, APIKey: "temporary-fixture-key"}
}

// Moved from server/api: the publication state machine now belongs here.
func TestConcurrentPublicationRevisionsDoNotRegress(t *testing.T) {
	var p publication
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.publish(true) }()
	}
	wg.Wait()
	got := p.snapshot()
	if got.DesiredRevision != 64 || got.AppliedRevision != 64 {
		t.Fatalf("lost or regressed publication revision: %+v", got)
	}
}

func TestConfigurationSavedFailureRetainsDataAndApplyDoesNotSaveAgain(t *testing.T) {
	st := configurationTestStore(t)
	fail := false
	var publications int
	service := NewConfiguration(st, func() error {
		publications++
		if fail {
			return errors.New("private-publication-detail")
		}
		return nil
	}, nil)
	before := service.Status()
	fail = true
	channel, change, err := service.CreateChannel(configurationChannel("saved-before-publication"))
	if err != nil || channel == nil || !change.Saved {
		t.Fatalf("save failed: change=%+v err=%v", change, err)
	}
	if change.Publication.DesiredRevision != before.Channels.DesiredRevision+1 || change.Publication.AppliedRevision != before.Channels.AppliedRevision || change.Publication.LastError == "" || strings.Contains(change.Publication.LastError, "private-publication-detail") {
		t.Fatalf("publication failure lost or leaked: %+v", change.Publication)
	}
	saved, err := st.GetChannel(channel.ID)
	if err != nil || saved.Name != "saved-before-publication" {
		t.Fatal("publication failure rolled back saved data")
	}
	fail = false
	for range 2 {
		status := service.Apply()
		if status.Channels.DesiredRevision != change.Publication.DesiredRevision || status.Channels.AppliedRevision != status.Channels.DesiredRevision || status.Channels.LastError != "" {
			t.Fatalf("apply did not retry the saved revision: %+v", status)
		}
	}
	if publications != 4 || len(st.ListChannels()) != 1 || len(st.ListCredentials(channel.ID)) != 1 {
		t.Fatalf("apply duplicated a save: publications=%d", publications)
	}
}

func TestConfigurationRejectedSaveDoesNotPublish(t *testing.T) {
	st := configurationTestStore(t)
	var channelPublications, pricePublications int
	service := NewConfiguration(st, func() error { channelPublications++; return nil }, func() error { pricePublications++; return nil })
	before := service.Status()
	invalid := configurationChannel("invalid")
	invalid.APIKey = ""
	_, change, err := service.CreateChannel(invalid)
	if err == nil || change.Saved {
		t.Fatal("invalid channel was reported saved")
	}
	_, change, err = service.UpsertPrice("m1", &store.PriceInput{InputPricePer1K: -1})
	if err == nil || change.Saved {
		t.Fatal("invalid price was reported saved")
	}
	after := service.Status()
	if before != after || channelPublications != 1 || pricePublications != 1 || len(st.ListChannels()) != 0 {
		t.Fatalf("rejected write changed publication state: before=%+v after=%+v", before, after)
	}
}

func TestConfigurationNilMutationInputsDoNotSaveOrPublish(t *testing.T) {
	st := configurationTestStore(t)
	var publications int
	service := NewConfiguration(st, func() error { publications++; return nil }, nil)
	channel, _, err := service.CreateChannel(configurationChannel("unchanged"))
	if err != nil {
		t.Fatal(err)
	}
	credentials := st.ListCredentials(channel.ID)
	if len(credentials) != 1 {
		t.Fatal("fixture credential missing")
	}
	before, calls := service.Status(), publications
	for _, tc := range []struct {
		name   string
		mutate func() (Change, error)
	}{
		{"create_channel", func() (Change, error) { _, change, err := service.CreateChannel(nil); return change, err }},
		{"update_channel", func() (Change, error) { _, change, err := service.UpdateChannel(channel.ID, nil); return change, err }},
		{"create_credential", func() (Change, error) {
			_, change, err := service.CreateCredential(channel.ID, nil)
			return change, err
		}},
		{"update_credential", func() (Change, error) {
			_, change, err := service.UpdateCredential(credentials[0].ID, nil)
			return change, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change, err := tc.mutate()
			if err == nil || change.Saved || publications != calls || service.Status() != before {
				t.Fatalf("nil input changed configuration: change=%+v err=%v publications=%d", change, err, publications)
			}
		})
	}
	saved, err := st.GetChannel(channel.ID)
	if err != nil || saved.Name != "unchanged" || len(st.ListChannels()) != 1 || len(st.ListCredentials(channel.ID)) != 1 {
		t.Fatal("nil mutation changed persisted channel or credentials")
	}
}

func TestConfigurationCommittedCacheFailureAdvancesOnce(t *testing.T) {
	st := configurationTestStore(t)
	service := NewConfiguration(st, func() error { _, err := st.ChannelSnapshot(); return err }, nil)
	key, err := st.CreateToken(&store.TokenInput{Name: "corrupt-cache-fixture", Key: "temporary-token-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Table("tokens").Where("id = ?", key.ID).Update("models", "invalid-json").Error; err != nil {
		t.Fatal(err)
	}
	before := service.Status()
	_, change, err := service.CreateChannel(configurationChannel("committed"))
	var savedError *store.SavedConfigurationError
	if !errors.As(err, &savedError) || !change.Saved {
		t.Fatalf("committed cache error lost: change=%+v err=%v", change, err)
	}
	if change.Publication.DesiredRevision != before.Channels.DesiredRevision+1 || change.Publication.AppliedRevision != change.Publication.DesiredRevision || len(st.ListChannels()) != 1 {
		t.Fatalf("committed save was not published once: %+v", change)
	}
	status := service.Apply()
	if status.Channels.DesiredRevision != change.Publication.DesiredRevision || len(st.ListChannels()) != 1 {
		t.Fatal("retry duplicated committed save or desired revision")
	}
}

type signalingConfigurationStore struct {
	*store.Store
	priceEntered chan struct{}
}

func (s *signalingConfigurationStore) UpsertModelPrice(price *model.ModelPrice) error {
	s.priceEntered <- struct{}{}
	return s.Store.UpsertModelPrice(price)
}

func TestConfigurationCrossResourceWriteAndApplyWaitForPublication(t *testing.T) {
	st := &signalingConfigurationStore{Store: configurationTestStore(t), priceEntered: make(chan struct{}, 1)}
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var blockNext atomic.Bool
	service := NewConfiguration(st, func() error {
		if blockNext.Swap(false) {
			close(entered)
			<-release
		}
		return nil
	}, nil)
	blockNext.Store(true)
	channelDone := make(chan error, 1)
	go func() {
		_, _, err := service.CreateChannel(configurationChannel("blocked-publication"))
		channelDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("channel publication did not start")
	}
	priceDone, applyDone := make(chan error, 1), make(chan ConfigStatus, 1)
	priceStarted, applyStarted := make(chan struct{}), make(chan struct{})
	go func() {
		close(priceStarted)
		_, _, err := service.UpsertPrice("m1", &store.PriceInput{InputPricePer1K: 1, OutputPricePer1K: 2})
		priceDone <- err
	}()
	go func() { close(applyStarted); applyDone <- service.Apply() }()
	<-priceStarted
	<-applyStarted
	select {
	case <-st.priceEntered:
		t.Fatal("price save crossed an in-flight channel save/publication")
	case <-applyDone:
		t.Fatal("apply crossed an in-flight channel save/publication")
	case <-time.After(50 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })
	for _, done := range []<-chan error{channelDone, priceDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("serialized write did not complete")
		}
	}
	select {
	case <-applyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("serialized apply did not complete")
	}
	status := service.Status()
	if status.Channels.DesiredRevision != 2 || status.Prices.DesiredRevision != 2 || status.Channels.AppliedRevision != 2 || status.Prices.AppliedRevision != 2 {
		t.Fatalf("serialized writes lost a revision: %+v", status)
	}
}
