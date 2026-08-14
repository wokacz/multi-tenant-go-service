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

// Sender delivers a password-reset code to an address.
type Sender interface {
	SendPasswordReset(ctx context.Context, to, code string) error
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

type smtpSender struct {
	cfg *config.Config
}

func (s *smtpSender) SendPasswordReset(_ context.Context, to, code string) error {
	from := s.cfg.SMTPFrom
	body := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: Your password reset code",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Your password reset code is " + code + ".",
		"It expires in 15 minutes. If you did not request this, ignore this message.",
	}, "\r\n")

	addr := net.JoinHostPort(s.cfg.SMTPHost, strconv.Itoa(s.cfg.SMTPPort))

	var auth smtp.Auth
	if s.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPassword, s.cfg.SMTPHost)
	}

	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(body)); err != nil {
		return fmt.Errorf("mail: send reset code: %w", err)
	}

	return nil
}
