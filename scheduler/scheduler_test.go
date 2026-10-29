package scheduler

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/store"
)

type fakeMailer struct {
	sent   int
	onSend func()
	err    error
}

func (m *fakeMailer) Send(_, _, _, _, _, _, _ string) error {
	if m.onSend != nil {
		m.onSend()
	}
	if m.err != nil {
		return m.err
	}
	m.sent++
	return nil
}

func (m *fakeMailer) SendVerification(_, _, _ string) error { return nil }

func setup(t *testing.T, mailer *fakeMailer) (*Scheduler, *store.DB, *store.Config, *store.Feed) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	user, _ := st.GetOrCreateUser(ctx, "fp", "key")
	cfg, err := st.CreateConfig(ctx, user.ID, "daily.txt", "a@example.com", "0 8 * * *", true, false, "=: email a@example.com", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := st.CreateFeed(ctx, cfg.ID, "https://example.invalid/feed.xml", "Example")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := st.EnsurePendingVerification(ctx, user.ID, "a@example.com")
	if err := st.MarkEmailVerified(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	return NewScheduler(st, mailer, log.New(io.Discard), time.Minute, "https://herald.test"), st, cfg, feed
}

func TestSendDigestRecordsEvenIfCallerCancelsMidSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mailer := &fakeMailer{onSend: cancel}
	s, st, cfg, feed := setup(t, mailer)

	results := []*FetchResult{{FeedID: feed.ID, FeedURL: feed.URL, Items: []FetchedItem{{GUID: "a", Title: "A", Link: "https://example.com/a"}}}}
	batch := s.collectNewItems(ctx, results)
	if err := s.sendDigest(ctx, cfg, batch); err != nil {
		t.Fatalf("sendDigest: %v", err)
	}

	seen, err := st.GetSeenGUIDs(context.Background(), feed.ID, []string{"a"})
	if err != nil || !seen["a"] {
		t.Fatalf("sent item must be marked seen after a cancelled send, got %v %v", seen, err)
	}
	if n, _ := st.CountRecentSends(context.Background(), cfg.ID, 1); n != 1 {
		t.Fatalf("send should be recorded, got %d", n)
	}
}

func TestFailedSendLeavesItemsUnseen(t *testing.T) {
	mailer := &fakeMailer{err: errors.New("smtp down")}
	s, st, cfg, feed := setup(t, mailer)
	ctx := context.Background()

	results := []*FetchResult{{FeedID: feed.ID, FeedURL: feed.URL, Items: []FetchedItem{{GUID: "a", Title: "A"}}}}
	if err := s.sendDigest(ctx, cfg, s.collectNewItems(ctx, results)); err == nil {
		t.Fatal("expected send error")
	}
	if seen, _ := st.GetSeenGUIDs(ctx, feed.ID, []string{"a"}); seen["a"] {
		t.Fatal("unsent item must stay unseen")
	}
}

func TestCollectNewItemsIgnoresFailedFeeds(t *testing.T) {
	s, _, _, feed := setup(t, &fakeMailer{})
	results := []*FetchResult{
		{FeedID: feed.ID, FeedURL: feed.URL, Error: errors.New("boom"), Items: []FetchedItem{{GUID: "x"}}},
	}
	batch := s.collectNewItems(context.Background(), results)
	if batch.total() != 0 || len(batch.seen) != 0 {
		t.Fatalf("failed feed must contribute nothing, got %+v", batch)
	}
}

func TestRunAdvancesScheduleWhenItFails(t *testing.T) {
	s, st, cfg, _ := setup(t, &fakeMailer{})
	ctx := context.Background()

	if _, err := s.run(ctx, cfg, nil); err == nil {
		t.Fatal("expected an error: the only feed is unreachable")
	}
	got, err := st.GetConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextRun.Valid || !got.NextRun.Time.After(time.Now()) {
		t.Fatalf("a failed run must move the config to its next tick, next_run = %v", got.NextRun)
	}
	due, _ := st.GetDueConfigs(ctx, time.Now().UTC())
	if len(due) != 0 {
		t.Fatal("a failed config must not be due again on the next scheduler tick")
	}
}

func TestRunRefusesOverlappingRunsOfOneConfig(t *testing.T) {
	s, _, cfg, _ := setup(t, &fakeMailer{})
	s.running.Store(cfg.ID, struct{}{})
	if _, err := s.run(context.Background(), cfg, nil); err == nil || err.Error() != "this digest is already running" {
		t.Fatalf("expected overlap refusal, got %v", err)
	}
}
