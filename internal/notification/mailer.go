// Package notification implements the outbox pattern for transactional email:
// triggers write notification docs synchronously; a background worker drains
// the outbox and sends via the Mailer interface (Gmail SMTP today, swappable
// for SES/Resend/Postmark later without touching service code).
package notification

import (
	"context"
	"fmt"
	"log/slog"

	"gopkg.in/gomail.v2"
)

// Message is one outbound email. Body is plain text — switch to multipart if
// HTML becomes a requirement.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer is the seam the worker uses. GmailMailer is the v1 implementation;
// a future ESP swap implements this same interface.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// GmailMailer talks to Gmail SMTP using an APP PASSWORD (NOT a regular Gmail
// password). 2-step verification must be enabled on the account to generate
// one. Recipient quota is ~500/day on free Gmail and 2,000/day on Workspace.
type GmailMailer struct {
	dialer *gomail.Dialer
	from   string
}

func NewGmailMailer(user, appPassword, host string, port int) *GmailMailer {
	d := gomail.NewDialer(host, port, user, appPassword)
	// gomail uses STARTTLS automatically on port 587.
	return &GmailMailer{dialer: d, from: user}
}

func (g *GmailMailer) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := gomail.NewMessage()
	m.SetHeader("From", g.from)
	m.SetHeader("To", msg.To)
	m.SetHeader("Subject", msg.Subject)
	m.SetBody("text/plain", msg.Body)
	if err := g.dialer.DialAndSend(m); err != nil {
		return fmt.Errorf("send via gmail: %w", err)
	}
	return nil
}

// LogMailer is a fallback used when GMAIL_USER / GMAIL_APP_PASSWORD are unset.
// Useful for local dev — emails show up in logs instead of failing the boot.
type LogMailer struct {
	logger *slog.Logger
}

func NewLogMailer(logger *slog.Logger) *LogMailer { return &LogMailer{logger: logger} }

func (l *LogMailer) Send(_ context.Context, msg Message) error {
	l.logger.Info("email_logged_only",
		slog.String("to", msg.To),
		slog.String("subject", msg.Subject),
		slog.String("body", msg.Body),
	)
	return nil
}
