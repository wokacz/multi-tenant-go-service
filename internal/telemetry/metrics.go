package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics is every instrument this product records by hand.
//
// Generic HTTP and SQL numbers come from the instrumentation and need nothing here.
// These are the ones that describe *this* product, and they are what somebody
// actually asks about a running installation: how many sign-ins are failing and
// why, which permission is refusing people, whether mail is going out. A dashboard
// of averages answers none of that.
//
// It is a struct of instruments rather than a map of names because a typo in a
// metric name is a series that silently never appears. Here it is a compile error.
type Metrics struct {
	// SignIns counts finished sign-in attempts by outcome: granted, a wrong
	// password, a second factor demanded, a suspended account, a revoked device.
	// The ratio between them is the health of the front door.
	SignIns metric.Int64Counter

	// AuthzDenials counts refusals by permission and scope. A permission that
	// refuses constantly is either misconfigured roles or a screen asking for
	// something it should not.
	AuthzDenials metric.Int64Counter

	// RateLimited counts requests turned away by the limiter, by route. This is the
	// one that told us inviting a team hit the registration budget; a number would
	// have said so before a person did.
	RateLimited metric.Int64Counter

	// Invitations counts the offer lifecycle: sent, accepted, declined, withdrawn.
	// Sent-minus-accepted is the number somebody is waiting on.
	Invitations metric.Int64Counter

	// MailFailures counts messages that could not be handed to the server, by kind.
	// Every one of them is a person who is not getting their code, and the handlers
	// deliberately do not fail the request over it — so without this the failure is
	// only a log line nobody has alerted on.
	MailFailures metric.Int64Counter

	// DBQueries and DBDuration describe storage from this side of the connection
	// pool, by operation and table.
	DBQueries  metric.Int64Counter
	DBDuration metric.Float64Histogram
}

// NewMetrics creates the instruments. It returns an error rather than panicking,
// because the meter is real and a bad instrument name is a mistake worth reporting
// at startup rather than at first use.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	var (
		m   Metrics
		err error
	)

	if m.SignIns, err = meter.Int64Counter("auth.sign_ins",
		metric.WithDescription("Finished sign-in attempts, by outcome"),
		metric.WithUnit("{attempt}")); err != nil {
		return nil, fmt.Errorf("telemetry: sign-in counter: %w", err)
	}

	if m.AuthzDenials, err = meter.Int64Counter("authz.denials",
		metric.WithDescription("Requests refused for lack of a permission"),
		metric.WithUnit("{denial}")); err != nil {
		return nil, fmt.Errorf("telemetry: denial counter: %w", err)
	}

	if m.RateLimited, err = meter.Int64Counter("http.rate_limited",
		metric.WithDescription("Requests turned away by the rate limiter"),
		metric.WithUnit("{request}")); err != nil {
		return nil, fmt.Errorf("telemetry: rate limit counter: %w", err)
	}

	if m.Invitations, err = meter.Int64Counter("orgs.invitations",
		metric.WithDescription("Invitation lifecycle events"),
		metric.WithUnit("{event}")); err != nil {
		return nil, fmt.Errorf("telemetry: invitation counter: %w", err)
	}

	if m.MailFailures, err = meter.Int64Counter("mail.failures",
		metric.WithDescription("Messages that could not be sent"),
		metric.WithUnit("{message}")); err != nil {
		return nil, fmt.Errorf("telemetry: mail failure counter: %w", err)
	}

	if m.DBQueries, err = meter.Int64Counter("db.queries",
		metric.WithDescription("Statements executed, by operation and table"),
		metric.WithUnit("{query}")); err != nil {
		return nil, fmt.Errorf("telemetry: query counter: %w", err)
	}

	if m.DBDuration, err = meter.Float64Histogram("db.query.duration",
		metric.WithDescription("How long statements took"),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("telemetry: query histogram: %w", err)
	}

	return &m, nil
}

// The attribute keys, named once. A counter split by "outcome" in one place and
// "result" in another is two series nobody can add together.
const (
	AttrOutcome    = "outcome"
	AttrPermission = "permission"
	AttrScope      = "scope"
	AttrRoute      = "route"
	AttrEvent      = "event"
	AttrKind       = "kind"
	AttrOperation  = "operation"
	AttrTable      = "table"
	AttrError      = "error"
)

// Sign-in outcomes, spelled once so the series are stable across releases.
const (
	OutcomeGranted          = "granted"
	OutcomeBadCredentials   = "bad_credentials"
	OutcomeTwoFactorNeeded  = "two_factor_required"
	OutcomeSuspended        = "suspended"
	OutcomeDeviceRevoked    = "device_revoked"
	OutcomeBadTwoFactorCode = "bad_two_factor_code"
	OutcomeError            = "error"
)

// Invitation lifecycle events.
const (
	EventInvitationSent      = "sent"
	EventInvitationAccepted  = "accepted"
	EventInvitationDeclined  = "declined"
	EventInvitationWithdrawn = "withdrawn"
	EventInvitationReissued  = "reissued"
)

// CountSignIn is a convenience for the one call site that has to classify an error
// into an outcome. Nil-safe, because the no-op Metrics from a failed instrument
// creation has nil fields and a metric must never be the reason a request fails.
func (m *Metrics) CountSignIn(ctx context.Context, outcome string) {
	m.add(ctx, m.SignIns, attribute.String(AttrOutcome, outcome))
}

// CountDenial records an authorization refusal.
func (m *Metrics) CountDenial(ctx context.Context, permission, scope string) {
	m.add(ctx, m.AuthzDenials,
		attribute.String(AttrPermission, permission),
		attribute.String(AttrScope, scope))
}

// CountRateLimited records a request the limiter turned away.
func (m *Metrics) CountRateLimited(ctx context.Context, route string) {
	m.add(ctx, m.RateLimited, attribute.String(AttrRoute, route))
}

// CountInvitation records one lifecycle event.
func (m *Metrics) CountInvitation(ctx context.Context, event string) {
	m.add(ctx, m.Invitations, attribute.String(AttrEvent, event))
}

// CountMailFailure records a message that did not go out.
func (m *Metrics) CountMailFailure(ctx context.Context, kind string) {
	m.add(ctx, m.MailFailures, attribute.String(AttrKind, kind))
}

func (m *Metrics) add(ctx context.Context, counter metric.Int64Counter, attrs ...attribute.KeyValue) {
	if m == nil || counter == nil {
		return
	}

	counter.Add(ctx, 1, metric.WithAttributes(attrs...))
}
