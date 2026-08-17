// Package mail delivers password-reset codes, two-factor codes, and invitations.
// Production talks to an SMTP server. Development with SMTP unset writes a
// notice to the process log so a laptop can finish the flow without a mailer;
// one-time codes themselves go to stderr only when a TTY is attached or
// MAIL_LOG_CODES is set, so a log aggregator never sees them by accident.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
)

// Sender delivers the one-time codes and invitations this service emails.
type Sender interface {
	SendPasswordReset(ctx context.Context, to, code string) error
	SendTwoFactorCode(ctx context.Context, to, code string) error
	SendEmailChange(ctx context.Context, to, code string) error
	SendInvitation(ctx context.Context, to, orgName, token string, expiresAt time.Time) error
}

// New picks SMTP when a host is configured, and the development logger
// otherwise. Production never reaches here without a host — config.Load
// rejects that combination.
func New(cfg *config.Config, log *slog.Logger) Sender {
	if cfg.SMTPHost != "" {
		return &smtpSender{cfg: cfg}
	}

	return &logSender{log: log, codes: cfg.MailLogCodes || stderrIsTTY()}
}

func stderrIsTTY() bool {
	info, err := os.Stderr.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

type logSender struct {
	log   *slog.Logger
	codes bool
}

func (s *logSender) SendPasswordReset(_ context.Context, to, code string) error {
	s.note("password reset", to, code)

	return nil
}

func (s *logSender) SendTwoFactorCode(_ context.Context, to, code string) error {
	s.note("two-factor", to, code)

	return nil
}

func (s *logSender) SendEmailChange(_ context.Context, to, code string) error {
	s.note("email change", to, code)

	return nil
}

func (s *logSender) SendInvitation(_ context.Context, to, orgName, token string, expiresAt time.Time) error {
	s.log.Info("organization invitation (SMTP is not configured)",
		"email", to, "organization", orgName, "expires_at", expiresAt)

	// The token follows the same rule as the one-time codes: never to the
	// structured log, and to stderr only where that was declared safe.
	s.note("invitation", to, token)

	return nil
}

// note writes the existence of a code to the structured log always, and the
// code itself only to stderr when that is safe. slog is what aggregators
// collect; a shared staging log that contained reset codes would be a way to
// take over any account whose address an operator could guess.
func (s *logSender) note(kind, to, code string) {
	s.log.Info(kind+" code requested (SMTP is not configured)", "email", to)

	if !s.codes {
		return
	}

	fmt.Fprintf(os.Stderr, "%s code (SMTP is not configured) email=%s code=%s\n", kind, to, code)
}

type smtpSender struct {
	cfg *config.Config
}

func (s *smtpSender) SendPasswordReset(_ context.Context, to, code string) error {
	return s.send(to, "Your password reset code",
		"Your password reset code is "+code+".",
		"It expires in 15 minutes. If you did not request this, ignore this message.")
}

func (s *smtpSender) SendTwoFactorCode(_ context.Context, to, code string) error {
	return s.send(to, "Your sign-in code",
		"Your sign-in code is "+code+".",
		"It expires in 10 minutes. If you are not signing in right now, change your password.")
}

// SendEmailChange goes to the *new* address, which is the point: the code proves
// somebody reads that mailbox before the account starts using it.
func (s *smtpSender) SendEmailChange(_ context.Context, to, code string) error {
	return s.send(to, "Confirm your new email address",
		"Your confirmation code is "+code+".",
		"It expires in 15 minutes. If you did not ask to change your address, ignore this message.")
}

// SendInvitation states the address the offer was issued to, because accepting
// requires the account to have that address and somebody reading the message in a
// forwarded mailbox otherwise has no way to know why it was refused.
func (s *smtpSender) SendInvitation(_ context.Context, to, orgName, token string, expiresAt time.Time) error {
	return s.send(to, "You have been invited to "+orgName,
		"You have been invited to join "+orgName+".",
		"Sign in, or register with "+to+" — the invitation can only be accepted by an "+
			"account with that address — then accept it with this token: "+token+
			"\r\n\r\nIt expires on "+expiresAt.Format(time.RFC1123)+".")
}

// send builds and posts one plain-text message.
//
// The header lines are assembled from configuration and from a subject this
// package chooses — never from request input. Interpolating a caller-supplied
// string into a header is how a newline in an address turns into an injected
// Bcc, and the only variable parts here are the code (six digits) and the
// organization name (validated before it is stored).
func (s *smtpSender) send(to, subject string, lines ...string) error {
	from := s.cfg.SMTPFrom
	body := strings.Join(append([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
	}, lines...), "\r\n")

	addr := net.JoinHostPort(s.cfg.SMTPHost, strconv.Itoa(s.cfg.SMTPPort))

	var auth smtp.Auth
	if s.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPassword, s.cfg.SMTPHost)
	}

	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(body)); err != nil {
		return fmt.Errorf("mail: send %q: %w", subject, err)
	}

	return nil
}
