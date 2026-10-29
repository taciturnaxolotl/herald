// Package verify sends confirmed opt-in emails without letting the prompt
// itself be abused to flood an inbox.
package verify

import (
	"context"
	"fmt"
	"time"

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/ratelimit"
	"github.com/taciturnaxolotl/herald/store"
)

// A destination address gets at most one prompt per cooldown regardless of
// source; the per-IP cap is the meaningful one since fresh keys are free.
const (
	emailCooldown = 24 * time.Hour
	ipPerDay      = 5
	fpPerDay      = 3
	secondsPerDay = 86400.0
)

// Status is the outcome of a confirmation-email request.
type Status int

const (
	AlreadyVerified Status = iota // address already confirmed, nothing to do
	Sent                          // confirmation email sent
	Pending                       // a recent confirmation is still outstanding (cooldown)
	Throttled                     // per-IP or per-fingerprint cap hit
)

func (s Status) String() string {
	switch s {
	case AlreadyVerified:
		return "already-verified"
	case Sent:
		return "sent"
	case Pending:
		return "pending"
	case Throttled:
		return "throttled"
	default:
		return "unknown"
	}
}

// Mailer delivers confirmation emails.
type Mailer interface {
	SendVerification(to, fingerprint, verifyURL string) error
}

type Service struct {
	store   *store.DB
	mailer  Mailer
	logger  *log.Logger
	origin  string
	ipLimit *ratelimit.Limiter
	fpLimit *ratelimit.Limiter
}

func New(st *store.DB, mailer Mailer, logger *log.Logger, origin string) *Service {
	return &Service{
		store:   st,
		mailer:  mailer,
		logger:  logger,
		origin:  origin,
		ipLimit: ratelimit.New(ipPerDay/secondsPerDay, ipPerDay),
		fpLimit: ratelimit.New(fpPerDay/secondsPerDay, fpPerDay),
	}
}

// Request sends a confirmation email for (user, email) unless it is already
// verified or a limit blocks it, so it is safe to call on every upload.
func (s *Service) Request(ctx context.Context, userID int64, email, fingerprint, ipKey string) (Status, error) {
	verified, err := s.store.IsEmailVerified(ctx, userID, email)
	if err != nil {
		return Throttled, fmt.Errorf("check verified: %w", err)
	}
	if verified {
		return AlreadyVerified, nil
	}

	v, err := s.store.EnsurePendingVerification(ctx, userID, email)
	if err != nil {
		return Throttled, fmt.Errorf("ensure pending: %w", err)
	}

	// Checked before spending limiter tokens.
	last, err := s.store.LastConfirmationToEmail(ctx, email)
	if err != nil {
		return Throttled, fmt.Errorf("last confirmation: %w", err)
	}
	if last.Valid && time.Since(last.Time) < emailCooldown {
		return Pending, nil
	}

	if ipKey != "" && !s.ipLimit.Allow("verify:ip:"+ipKey) {
		return Throttled, nil
	}
	if !s.fpLimit.Allow("verify:fp:" + fingerprint) {
		return Throttled, nil
	}

	if !v.Token.Valid {
		return Throttled, fmt.Errorf("pending verification has no token")
	}
	if err := s.mailer.SendVerification(email, fingerprint, s.origin+"/verify/"+v.Token.String); err != nil {
		return Throttled, fmt.Errorf("send verification: %w", err)
	}
	if err := s.store.MarkConfirmationSent(ctx, v.ID, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to record confirmation send", "err", err)
	}
	return Sent, nil
}
