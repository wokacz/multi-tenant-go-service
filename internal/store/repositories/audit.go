package repositories

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

var (
	_ audit.Reader         = (*Orgs)(nil)
	_ audit.PlatformReader = (*Orgs)(nil)
)

// record writes one audit row on the transaction that is making the change.
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
func record(ctx context.Context, tx *gorm.DB, event *models.AuthzEvent) error {
	actor := audit.ActorFrom(ctx)
	if actor.IsZero() {
		return nil
	}

	event.ActorID = actor.ID
	event.IP = actor.IP
	event.UserAgent = truncateUserAgent(actor.UserAgent)

	// The address is NOT NULL and typed inet, so a request that reached here
	// without one — a test, a job — needs something valid rather than a
	// constraint violation that hides the change it was describing.
	if event.IP == "" {
		event.IP = "0.0.0.0"
	}

	if err := tx.Create(event).Error; err != nil {
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
	return r.events(ctx, func(q *gorm.DB) *gorm.DB {
		return q.Where("e.organization_id = ?", orgID)
	}, limit, offset)
}

func (r *Orgs) AllEvents(ctx context.Context, limit, offset int) ([]audit.Event, error) {
	return r.events(ctx, func(q *gorm.DB) *gorm.DB { return q }, limit, offset)
}

// events runs the one query both readers share.
//
// The joins to users are LEFT: an account may have been deleted since, and
// dropping its entries from the history would quietly erase the very changes
// somebody is most likely to be looking for.
func (r *Orgs) events(
	ctx context.Context,
	scope func(*gorm.DB) *gorm.DB,
	limit, offset int,
) ([]audit.Event, error) {
	var rows []eventRow

	q := r.db.WithContext(ctx).
		Table("authz_events AS e").
		Select(`e.id, e.created_at, e.organization_id, e.action, e.role_id,
			e.permission_key, e.detail, e.ip, e.user_agent,
			e.actor_id, actor.name AS actor_name, actor.email AS actor_email,
			e.subject_id, subject.name AS subject_name, subject.email AS subject_email,
			COALESCE(r.key, '') AS role_key`).
		Joins("LEFT JOIN users actor ON actor.id = e.actor_id").
		Joins("LEFT JOIN users subject ON subject.id = e.subject_id").
		Joins("LEFT JOIN roles r ON r.id = e.role_id").
		Order("e.created_at DESC, e.id DESC").
		Limit(limit).
		Offset(offset)

	if err := scope(q).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: audit events: %w", err)
	}

	out := make([]audit.Event, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toEvent())
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

// recordAboutMember fills in the subject from a membership before writing.
//
// The audit is about people, and a membership id means nothing to somebody
// reading the history a year later. Resolving it here rather than at read time
// also means the entry survives the membership being deleted, which is exactly
// the case "who removed them" is asked about.
func recordAboutMember(
	ctx context.Context,
	tx *gorm.DB,
	orgID, memberID uuid.UUID,
	event *models.AuthzEvent,
) error {
	if audit.ActorFrom(ctx).IsZero() {
		return nil
	}

	var membership models.Membership

	err := tx.Select("user_id").
		First(&membership, "id = ? AND organization_id = ?", memberID, orgID).Error
	if err != nil {
		return fmt.Errorf("store: audit subject: %w", err)
	}

	// Every membership has an account behind it now, so there is no longer a case
	// where the subject is unknown and the address has to stand in for it.
	subject := membership.UserID
	event.SubjectID = &subject

	return record(ctx, tx, event)
}
