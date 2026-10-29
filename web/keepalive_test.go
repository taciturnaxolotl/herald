package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/store"
)

func newTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, "", "https://herald.test", 22, log.New(io.Discard), "test"), st
}

func TestKeepAliveOnlyRenewsOnPost(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	user, _ := st.GetOrCreateUser(ctx, "fp", "key")
	cfg, err := st.CreateConfig(ctx, user.ID, "daily.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEmailSend(ctx, cfg.ID, "a@example.com", "subject", token); err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	srv.mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/keep-alive/"+token, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "<form method=\"POST\">") {
		t.Fatalf("GET should show a confirm form, got %d", get.Code)
	}
	if got, _ := st.GetConfigByID(ctx, cfg.ID); got.LastActiveAt.Valid {
		t.Fatal("GET must not renew the digest")
	}

	post := httptest.NewRecorder()
	srv.mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/keep-alive/"+token, nil))
	if post.Code != http.StatusOK {
		t.Fatalf("POST status = %d", post.Code)
	}
	if got, _ := st.GetConfigByID(ctx, cfg.ID); !got.LastActiveAt.Valid {
		t.Fatal("POST should renew the digest")
	}

	bad := httptest.NewRecorder()
	srv.mux.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/keep-alive/nope", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("unknown token status = %d, want 404", bad.Code)
	}
}
