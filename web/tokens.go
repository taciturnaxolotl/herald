package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/taciturnaxolotl/herald/store"
)

type unsubscribePageData struct {
	Token            string
	ShortFingerprint string
	Filename         string
	Success          bool
	Message          string
	Error            string
}

func (s *Server) handleUnsubscribeGET(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ctx := r.Context()

	cfg, err := s.store.GetConfigByToken(ctx, token)
	if err != nil {
		s.fail(w, err, "get config by token", "Invalid Link", "This unsubscribe link is invalid.")
		return
	}

	user, err := s.store.GetUserByID(ctx, cfg.UserID)
	if err != nil {
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	shortFP := short(user.PubkeyFP, shortFingerprintLen)

	data := unsubscribePageData{
		Token:            token,
		ShortFingerprint: shortFP,
		Filename:         cfg.Filename,
	}

	s.render(w, http.StatusOK, "unsubscribe.html", data)
}

func (s *Server) handleUnsubscribePOST(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// RFC 8058 one-click unsubscribe pauses the digest and expects a plain reply.
	oneClick := r.FormValue("List-Unsubscribe") == "One-Click"
	action := store.PauseConfig
	if !oneClick {
		switch r.FormValue("action") {
		case "deactivate":
		case "delete":
			action = store.DeleteAccount
		default:
			http.Error(w, "Invalid action", http.StatusBadRequest)
			return
		}
	}

	cfg, err := s.store.RedeemUnsubscribeToken(r.Context(), token, action)
	if err != nil {
		switch {
		case !errors.Is(err, sql.ErrNoRows):
			s.logger.Error("redeem unsubscribe token", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		case oneClick:
			http.Error(w, "Invalid token", http.StatusNotFound)
		default:
			s.notFound(w, "Invalid Link", "This unsubscribe link is invalid or has expired.")
		}
		return
	}

	var message string
	if action == store.DeleteAccount {
		message = "All data deleted. You have been completely removed from Herald."
		s.logger.Info("user deleted", "user_id", cfg.UserID)
	} else {
		message = fmt.Sprintf("Config '%s' deactivated. You will no longer receive emails for this config. Other configs remain active. Files remain accessible via SSH/SCP.", cfg.Filename)
		s.logger.Info("config deactivated", "config_id", cfg.ID, "filename", cfg.Filename, "one_click", oneClick)
	}

	if oneClick {
		_, _ = w.Write([]byte("Unsubscribed"))
		return
	}

	data := unsubscribePageData{Success: true, Message: message}
	s.render(w, http.StatusOK, "unsubscribe.html", data)
}

type keepAlivePageData struct {
	Filename  string
	ExpiresAt string
	Success   bool
}

// handleKeepAlive shows a confirm button on GET and renews only on POST, so
// mail scanners that prefetch links cannot keep every digest alive.
func (s *Server) handleKeepAlive(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ctx := r.Context()

	cfg, err := s.store.GetConfigByTrackingToken(ctx, token)
	if err != nil {
		s.fail(w, err, "get config by tracking token", "Invalid Link", "This keep-alive link is invalid.")
		return
	}

	data := keepAlivePageData{
		Filename:  cfg.Filename,
		ExpiresAt: time.Now().AddDate(0, 0, store.InactivityDays).Format("January 2, 2006"),
	}

	if r.Method == http.MethodPost {
		if err := s.store.UpdateLastActive(ctx, token); err != nil {
			s.logger.Error("keep-alive", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		data.Success = true
	}

	s.render(w, http.StatusOK, "keepalive.html", data)
}

type verifyPageData struct {
	Token            string
	Email            string
	ShortFingerprint string
	Success          bool
}

func (s *Server) handleVerifyGET(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ctx := r.Context()

	v, err := s.store.GetVerificationByToken(ctx, token, store.VerifyTokenTTL)
	if err != nil {
		s.fail(w, err, "get verification by token", "Invalid Link", "This confirmation link is invalid or has expired.")
		return
	}

	shortFP := ""
	if user, err := s.store.GetUserByID(ctx, v.UserID); err == nil {
		shortFP = short(user.PubkeyFP, shortFingerprintLen)
	}

	// A GET is only a landing page; confirmation happens on POST so that link
	// prefetchers (mail scanners, SafeLinks) cannot auto-confirm.
	data := verifyPageData{
		Token:            token,
		Email:            v.Email,
		ShortFingerprint: shortFP,
	}
	s.render(w, http.StatusOK, "verify.html", data)
}

func (s *Server) handleVerifyPOST(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	v, err := s.store.ConfirmVerification(r.Context(), token)
	if err != nil {
		s.fail(w, err, "confirm verification", "Invalid Link", "This confirmation link is invalid or has expired.")
		return
	}

	s.logger.Info("email verified", "user_id", v.UserID, "email", v.Email)

	data := verifyPageData{Email: v.Email, Success: true}
	s.render(w, http.StatusOK, "verify.html", data)
}
