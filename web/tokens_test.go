package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUnsubscribeDeleteRemovesAccount(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	user, _ := st.GetOrCreateUser(ctx, "fp", "key")
	cfg, err := st.CreateConfig(ctx, user.ID, "daily.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"action": {"delete"}}
	req := httptest.NewRequest(http.MethodPost, "/unsubscribe/"+token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if _, err := st.GetUserByID(ctx, user.ID); err != sql.ErrNoRows {
		t.Fatalf("user should be gone, got %v", err)
	}
}

func TestOneClickUnsubscribePausesAndSpendsToken(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	user, _ := st.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := st.CreateConfig(ctx, user.ID, "daily.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	token, _ := st.GetOrCreateUnsubscribeToken(ctx, cfg.ID)

	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/unsubscribe/"+token, strings.NewReader("List-Unsubscribe=One-Click"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		srv.routeHandler(rec, req)
		return rec.Code
	}

	if code := post(); code != http.StatusOK {
		t.Fatalf("first one-click status = %d", code)
	}
	if got, _ := st.GetConfigByID(ctx, cfg.ID); got.NextRun.Valid {
		t.Fatal("digest should be paused")
	}
	if code := post(); code != http.StatusNotFound {
		t.Fatalf("spent token status = %d, want 404", code)
	}
}

func TestVerifyPostActivatesDigests(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	user, _ := st.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := st.CreateConfig(ctx, user.ID, "daily.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	_ = st.SetNextRun(ctx, cfg.ID, nil)
	v, err := st.EnsurePendingVerification(ctx, user.ID, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	srv.routeHandler(get, httptest.NewRequest(http.MethodGet, "/verify/"+v.Token.String, nil))
	if ok, _ := st.IsEmailVerified(ctx, user.ID, "a@example.com"); ok {
		t.Fatal("GET must not verify")
	}

	post := httptest.NewRecorder()
	srv.routeHandler(post, httptest.NewRequest(http.MethodPost, "/verify/"+v.Token.String, nil))
	if post.Code != http.StatusOK {
		t.Fatalf("POST status = %d", post.Code)
	}
	if ok, _ := st.IsEmailVerified(ctx, user.ID, "a@example.com"); !ok {
		t.Fatal("POST should verify")
	}
	if got, _ := st.GetConfigByID(ctx, cfg.ID); !got.NextRun.Valid {
		t.Fatal("verified digest should be scheduled")
	}
}
