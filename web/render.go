package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/taciturnaxolotl/herald/store"
)

// render executes a template into a buffer first, so a failure becomes a clean
// 500 instead of a half-written page.
func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render template", "template", name, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) notFound(w http.ResponseWriter, title, message string) {
	s.render(w, http.StatusNotFound, "404.html", struct{ Title, Message string }{title, message})
}

// fail answers a failed lookup: 404 for a missing row, nothing for a client
// that went away, 500 for anything else.
func (s *Server) fail(w http.ResponseWriter, err error, op, notFoundTitle, notFoundMsg string) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.notFound(w, notFoundTitle, notFoundMsg)
	case errors.Is(err, context.Canceled):
	default:
		s.logger.Error(op, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// lookupConfig resolves a public /fingerprint/filename path to its config.
func (s *Server) lookupConfig(w http.ResponseWriter, r *http.Request, fingerprint, filename string) (*store.Config, bool) {
	user, err := s.store.GetUserByFingerprint(r.Context(), fingerprint)
	if err != nil {
		s.fail(w, err, "get user", "", "")
		return nil, false
	}
	cfg, err := s.store.GetConfig(r.Context(), user.ID, filename)
	if err != nil {
		s.fail(w, err, "get config", "", "")
		return nil, false
	}
	return cfg, true
}

// short truncates a fingerprint or hash for display.
func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
