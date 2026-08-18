package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

var (
	_ audit.Reader         = (*Orgs)(nil)
	_ audit.PlatformReader = (*Orgs)(nil)
)

// recordEnt writes one audit row on the transaction that is making the change.
//
// Taking tx rather than the pool is the whole point. An audit row committed
// separately from the change it describes is the row that goes missing exactly
// when it matters: the change rolls back and the log says it happened, or the
// change commits and the log is empty because the second statement failed.
// Sharing the transaction makes the two atomic by construction.
//
// A missing actor writes nothing. That is deliberate — see audit.Actor — and it
// is why the guard against a forgotten one is a test that calls each mutating
// endpoint and demands a row, rather than a nil check here.
func recordEnt(ctx context.Context, tx *ent.Tx, event *models.AuthzEvent) error {
	actor := audit.ActorFrom(ctx)
	if actor.IsZero() {
		return nil
	}

	ip := actor.IP

	// The address is NOT NULL and typed inet, so a request that reached here
	// without one — a test, a job — needs something valid rather than a
	// constraint violation that hides the change it was describing.
	if ip == "" {
		ip = "0.0.0.0"
	}

	_, err := tx.AuthzEvent.Create().
		SetActorID(actor.ID).
		SetIP(ip).
		SetUserAgent(truncateUserAgent(actor.UserAgent)).
		SetAction(string(event.Action)).
		SetNillableOrganizationID(event.OrganizationID).
		SetNillableSubjectID(event.SubjectID).
		SetNillableRoleID(event.RoleID).
		SetPermissionKey(event.PermissionKey).
		SetDetail(event.Detail).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: record authz event: %w", err)
	}

	return nil
}

// maxUserAgentLength matches the column. The domain truncates its own copies;
// this is the audit path's.
const maxUserAgentLength = 512

func truncateUserAgent(s string) string {
	if len(s) <= maxUserAgentLength {
		return s
	}

	return s[:maxUserAgentLength]
}

// eventRow is the flat shape the history queries scan into. The actor and the
// subject are joined here rather than looked up per row, which is the
// difference between one query and one per entry.
type eventRow struct {
	ID             uuid.UUID
	CreatedAt      time.Time
	OrganizationID *uuid.UUID
	Action         models.AuthzAction
	RoleID         *uuid.UUID
	RoleKey        string
	PermissionKey  string
	Detail         string
	IP             string
	UserAgent      string

	ActorID    uuid.UUID
	ActorName  string
	ActorEmail string

	SubjectID    *uuid.UUID
	SubjectName  string
	SubjectEmail string
}

func (r *Orgs) Events(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]audit.Event, error) {
	return r.events(ctx, "WHERE e.organization_id = $3", limit, offset, orgID)
}

func (r *Orgs) AllEvents(ctx context.Context, limit, offset int) ([]audit.Event, error) {
	return r.events(ctx, "", limit, offset)
}

// events runs the one query both readers share.
//
// Raw SQL, deliberately, and the port to ent did not change that: three LEFT JOINs with
// aliased columns is what this query *is*, and an ORM that carried it added nothing but
// a place for it to be misread. It runs on the same pool as everything else, so it
// shares the connection limits and — once the driver is wrapped — the same spans.
//
// The joins to users are LEFT: an account may have been deleted since, and dropping its
// entries from the history would quietly erase the very changes somebody is most likely
// to be looking for. That also means the joined columns can be NULL, which is why the
// scan below reads them through sql.NullString rather than into plain strings — GORM
// used to fold a NULL into "" on the way past, and nothing else does.
func (r *Orgs) events(
	ctx context.Context,
	where string,
	limit, offset int,
	args ...any,
) ([]audit.Event, error) {
	const query = `
		SELECT e.id, e.created_at, e.organization_id, e.action, e.role_id,
		       e.permission_key, e.detail, e.ip, e.user_agent,
		       e.actor_id, actor.name, actor.email,
		       e.subject_id, subject.name, subject.email,
		       COALESCE(r.key, '')
		FROM authz_events AS e
		LEFT JOIN users actor ON actor.id = e.actor_id
		LEFT JOIN users subject ON subject.id = e.subject_id
		LEFT JOIN roles r ON r.id = e.role_id
		%s
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $1 OFFSET $2`

	// $1 and $2 are the page, so a scope's own parameter starts at $3 — see Events.
	params := append([]any{limit, offset}, args...)

	rows, err := r.db.SQL().QueryContext(ctx, fmt.Sprintf(query, where), params...)
	if err != nil {
		return nil, fmt.Errorf("store: audit events: %w", err)
	}

	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("store: audit events: close: %w", cerr)
		}
	}()

	out := make([]audit.Event, 0, limit)

	for rows.Next() {
		var (
			row                              eventRow
			actorName, actorEmail            sql.NullString
			subjectName, subjectEmail        sql.NullString
			permissionKey, detail, userAgent sql.NullString
		)

		err := rows.Scan(
			&row.ID, &row.CreatedAt, &row.OrganizationID, &row.Action, &row.RoleID,
			&permissionKey, &detail, &row.IP, &userAgent,
			&row.ActorID, &actorName, &actorEmail,
			&row.SubjectID, &subjectName, &subjectEmail,
			&row.RoleKey,
		)
		if err != nil {
			return nil, fmt.Errorf("store: audit events: scan: %w", err)
		}

		row.ActorName, row.ActorEmail = actorName.String, actorEmail.String
		row.SubjectName, row.SubjectEmail = subjectName.String, subjectEmail.String
		row.PermissionKey, row.Detail, row.UserAgent = permissionKey.String, detail.String, userAgent.String

		out = append(out, row.toEvent())
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: audit events: %w", err)
	}

	return out, nil
}

func (e *eventRow) toEvent() audit.Event {
	event := audit.Event{
		ID:             e.ID,
		OrganizationID: e.OrganizationID,
		Action:         e.Action,
		Actor:          audit.Party{ID: e.ActorID, Name: e.ActorName, Email: e.ActorEmail},
		RoleID:         e.RoleID,
		RoleKey:        e.RoleKey,
		Detail:         e.Detail,
		IP:             e.IP,
		UserAgent:      e.UserAgent,
	}

	event.At = e.CreatedAt.UTC()

	if e.SubjectID != nil {
		event.Subject = &audit.Party{ID: *e.SubjectID, Name: e.SubjectName, Email: e.SubjectEmail}
	}

	// The permission key is folded into the detail rather than given a field of
	// its own: it is only ever set alongside one, and a second nearly-empty
	// column would show up in every response for the sake of one action.
	if e.PermissionKey != "" && event.Detail == "" {
		event.Detail = e.PermissionKey
	}

	return event
}
