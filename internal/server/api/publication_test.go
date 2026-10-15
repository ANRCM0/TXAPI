package api

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

func TestPublicationFailureIsVisibleAndRetryDoesNotCreateRevision(t *testing.T) {
	engine, st := newAPITestEngine(t)
	_ = engine
	fail := false
	h := New(st, func() error {
		if fail {
			return errors.New("private-key-should-not-escape")
		}
		return nil
	}, nil)
	e := gin.New()
	h.Register(e.Group(""), middleware.AdminAuth(testSecret))
	fail = true
	req := httptest.NewRequest("PUT", "/api/channels/1", strings.NewReader(
		`{"name":"saved","type":"openai","base_url":"http://localhost:9911","models":["m1"],"status":"enabled","weight":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", testSecret)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("X-TXAPI-Publication-State") != "pending" {
		t.Fatalf("save must distinguish pending: %d %s", w.Code, w.Body)
	}
	status := h.configStatus()
	if status.Channels.DesiredRevision != 2 || status.Channels.AppliedRevision != 1 || status.Channels.LastError == "" {
		t.Fatalf("wrong failed publication state: %+v", status)
	}
	if strings.Contains(status.Channels.LastError, "private-key") {
		t.Fatal("private loader error escaped")
	}
	ch, err := st.GetChannel(1)
	if err != nil || ch.Name != "saved" {
		t.Fatal("failed publication should not roll back the saved database")
	}
	fail = false
	w = httptest.NewRecorder()
	context, _ := gin.CreateTestContext(w)
	h.ApplyConfiguration(context)
	if w.Code != 200 || h.configStatus().Channels.AppliedRevision != 2 || h.configStatus().Channels.DesiredRevision != 2 {
		t.Fatal("retry should apply the existing desired revision")
	}
}

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

func TestCommittedSaveWithFailedCacheRefreshStillAdvancesPublication(t *testing.T) {
	_, st := newAPITestEngine(t)
	h := New(st, func() error { _, err := st.ChannelSnapshot(); return err }, nil)
	key, err := st.CreateToken(&store.TokenInput{Name: "corrupt-cache-fixture", Key: "fixture-key-only"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Table("tokens").Where("id = ?", key.ID).Update("models", "invalid-json").Error; err != nil {
		t.Fatal(err)
	}
	e := gin.New()
	h.Register(e.Group(""), middleware.AdminAuth(testSecret))
	code, raw := doAPI(t, e, "POST", "/api/channels",
		`{"name":"committed","type":"openai","base_url":"http://localhost:9911","models":["m1"],"api_key":"fixture-only"}`, true)
	if code != 500 || !strings.Contains(raw, `"saved":true`) {
		t.Fatalf("partial save was indistinguishable from a rollback: %d %s", code, raw)
	}
	if h.configStatus().Channels.DesiredRevision != 2 || h.configStatus().Channels.AppliedRevision != 2 {
		t.Fatal("committed configuration was omitted from publication accounting")
	}
	if len(st.ListChannels()) != 2 {
		t.Fatal("publication did not refresh the independent channel cache")
	}
}
