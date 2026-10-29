package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoutes(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	user, _ := st.GetOrCreateUser(ctx, "SHA256:abcdefghijk", "key")
	if _, err := st.CreateConfig(ctx, user.ID, "news.txt", "a@example.com", "0 8 * * *", false, "raw config", time.Now()); err != nil {
		t.Fatal(err)
	}
	fp := user.PubkeyFP

	tests := []struct {
		method, path string
		want         int
	}{
		{"GET", "/", http.StatusOK},
		{"GET", "/style.css", http.StatusOK},
		{"GET", "/favicon.svg", http.StatusOK},
		{"GET", "/health", http.StatusOK},
		{"GET", "/" + fp, http.StatusOK},
		{"GET", "/" + fp + "/", http.StatusOK},
		{"GET", "/" + fp + "/news.txt", http.StatusOK},
		{"GET", "/" + fp + "/news.xml", http.StatusOK},
		{"GET", "/" + fp + "/news.json", http.StatusOK},
		{"GET", "/" + fp + "/missing.txt", http.StatusNotFound},
		{"GET", "/nobody", http.StatusNotFound},
		{"GET", "/a/b/c", http.StatusNotFound},
		{"GET", "/unsubscribe/nope", http.StatusNotFound},
		{"GET", "/verify/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
		}
	}
}

func TestOriginHost(t *testing.T) {
	for origin, want := range map[string]string{
		"https://herald.dunkirk.sh": "herald.dunkirk.sh",
		"http://localhost:8080":     "localhost",
		"http://[::1]:8080":         "::1",
		"":                          "localhost",
	} {
		if got := originHost(origin); got != want {
			t.Errorf("originHost(%q) = %q, want %q", origin, got, want)
		}
	}
}
