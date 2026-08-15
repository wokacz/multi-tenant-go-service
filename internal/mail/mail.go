// Package mail delivers password-reset codes. Production talks to an SMTP
// server. Development with SMTP unset writes the code to the process log so a
// laptop can finish the flow without a mailer.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/wokacz/go-example/internal/config"
)

// Sender delivers the one-time codes this service emails.
type Sender interface {
	SendPasswordReset(ctx context.Context, to, code string) error
	SendTwoFactorCode(ctx context.Context, to, code string) error
}

// New picks SMTP when a host is configured, and the development logger
// otherwise. Production never reaches here without a host — config.Load
// rejects that combination.
func New(cfg *config.Config, log *slog.Logger) Sender {
	if cfg.SMTPHost != "" {
		return &smtpSender{cfg: cfg}
	}

	return &logSender{log: log}
}

type logSender struct {
	log *slog.Logger
}

func (s *logSender) SendPasswordReset(_ context.Context, to, code string) error {
	s.log.Info("password reset code (SMTP is not configured)", "email", to, "code", code)

	return nil
}

func (s *logSender) SendTwoFactorCode(_ context.Context, to, code string) error {
	s.log.Info("two-factor code (SMTP is not configured)", "email", to, "code", code)

	return nil
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

// send builds and posts one plain-text message.
//
// The header lines are assembled from configuration and from a subject this
// package chooses — never from request input. Interpolating a caller-supplied
// string into a header is how a newline in an address turns into an injected
// Bcc, and the only variable part here is the code, which is six digits.
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
