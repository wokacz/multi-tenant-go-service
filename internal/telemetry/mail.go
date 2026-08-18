package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/wokacz/multi-tenant-go-service/internal/mail"
)

// MeteredMailer wraps a sender so every message is a span and every failure is a
// number.
//
// A decorator rather than instrumentation inside each handler: the handlers
// deliberately log a send failure and carry on — the row exists, the code can be
// reissued — so a failure that is only a log line is one nobody has alerted on.
// Doing it here means every caller is counted, including the ones added later.
//
// The recipient is *not* recorded. An address in a span attribute is an address in
// whatever stores spans, with a different retention policy and a different set of
// people who can read it.
func MeteredMailer(sender mail.Sender, tel *Telemetry) mail.Sender {
	if sender == nil {
		return nil
	}

	return &meteredMailer{inner: sender, tel: tel}
}

type meteredMailer struct {
	inner mail.Sender
	tel   *Telemetry
}

var _ mail.Sender = (*meteredMailer)(nil)

func (m *meteredMailer) SendPasswordReset(ctx context.Context, to, code string) error {
	return m.send(ctx, "password_reset", func(ctx context.Context) error {
		return m.inner.SendPasswordReset(ctx, to, code)
	})
}

func (m *meteredMailer) SendTwoFactorCode(ctx context.Context, to, code string) error {
	return m.send(ctx, "two_factor", func(ctx context.Context) error {
		return m.inner.SendTwoFactorCode(ctx, to, code)
	})
}

func (m *meteredMailer) SendEmailChange(ctx context.Context, to, code string) error {
	return m.send(ctx, "email_change", func(ctx context.Context) error {
		return m.inner.SendEmailChange(ctx, to, code)
	})
}

func (m *meteredMailer) SendInvitation(ctx context.Context, to, orgName, token string, expiresAt time.Time) error {
	return m.send(ctx, "invitation", func(ctx context.Context) error {
		return m.inner.SendInvitation(ctx, to, orgName, token, expiresAt)
	})
}

func (m *meteredMailer) send(ctx context.Context, kind string, do func(context.Context) error) error {
	ctx, span := m.tel.Tracer.Start(ctx, "mail "+kind,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(AttrKind, kind)))
	defer span.End()

	err := do(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "send failed")
		m.tel.Metrics.CountMailFailure(ctx, kind)
	}

	return err
}
