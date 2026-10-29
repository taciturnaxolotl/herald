package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFeedJSONListsSeenItemsAndCaches(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	user, _ := st.GetOrCreateUser(ctx, "SHA256:abcdefghijk", "key")
	cfg, _ := st.CreateConfig(ctx, user.ID, "news.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	feedA, _ := st.CreateFeed(ctx, cfg.ID, "https://a.example/feed.xml", "")
	feedB, _ := st.CreateFeed(ctx, cfg.ID, "https://b.example/feed.xml", "")
	_ = st.MarkItemSeen(ctx, feedA.ID, "a1", "A one", "https://a.example/1")
	_ = st.MarkItemSeen(ctx, feedB.ID, "b1", "B one", "https://b.example/1")
	_ = st.UpdateLastRun(ctx, cfg.ID, time.Now().UTC(), time.Now().UTC().Add(time.Hour))

	rec := httptest.NewRecorder()
	srv.routeHandler(rec, httptest.NewRequest(http.MethodGet, "/"+user.PubkeyFP+"/news.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var feed jsonFeed
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("expected items from both feeds, got %+v", feed.Items)
	}
	if feed.FeedURL != "https://herald.test/"+user.PubkeyFP+"/news.json" {
		t.Errorf("feed_url = %q", feed.FeedURL)
	}

	etag := rec.Header().Get("ETag")
	again := httptest.NewRequest(http.MethodGet, "/"+user.PubkeyFP+"/news.json", nil)
	again.Header.Set("If-None-Match", "W/"+etag)
	rec2 := httptest.NewRecorder()
	srv.routeHandler(rec2, again)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("matching weak ETag status = %d, want 304", rec2.Code)
	}
}
