// Package audit records who changed whose authority, and reads the record back.
//
// It exists as its own package rather than as part of orgs because the two have
// opposite shapes: orgs decides and mutates, audit only observes. Keeping the
// writing side to one helper and the reading side to one interface is what stops
// "log it too" being copied into a dozen call sites, each slightly different.
package audit

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// Actor is who is making the change, and from where.
//
// It travels on the context rather than as a parameter on every mutating
// method. That is the same choice the codebase already makes for the
// request-scoped logger and for the client's address: these are facts about the
// request, not arguments to a business rule, and threading them through twelve
// signatures makes every one of those signatures about auditing.
//
// The risk of an implicit value is that somebody forgets to set it. Two things
// answer that: the recorder writes nothing when the actor is missing, and
// TestEveryMutatingOperationIsAudited calls each mutating endpoint and fails if
// no row appears. The guard is on the outcome, which is what actually matters.
type Actor struct {
	ID        uuid.UUID
	IP        string
	UserAgent string
}

// IsZero reports whether there is nobody to attribute a change to.
func (a Actor) IsZero() bool {
	return a.ID == uuid.Nil
}

// ctxKey keeps the context key unexported so no other package can collide with
// it, which is the whole reason for the named type.
type ctxKey int

const actorKey ctxKey = iota

// WithActor attaches the person making the change.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey, actor)
}

// ActorFrom returns the person making the change, or the zero Actor.
//
// The zero value is what a background job or a test gets, and it is why the
// recorder skips rather than writing an anonymous row: an audit entry with no
// actor answers none of the questions an audit exists to answer.
func ActorFrom(ctx context.Context) Actor {
	actor, ok := ctx.Value(actorKey).(Actor)
	if !ok {
		return Actor{}
	}

	return actor
}

// Party is one side of a change — the person who made it, or the person it was
// about — with enough identity to render a line in a log without a second
// lookup.
type Party struct {
	ID    uuid.UUID
	Name  string
	Email string
}

// Event is one recorded change.
type Event struct {
	ID             uuid.UUID
	At             time.Time
	OrganizationID *uuid.UUID
	Action         models.AuthzAction

	Actor Party
	// Subject is absent for changes that are not about a person — editing a
	// role, renaming an organization.
	Subject *Party

	// RoleKey is captured at write time rather than joined at read time,
	// because the role may since have been deleted and "granted a role that no
	// longer exists" is exactly the entry somebody will be trying to read.
	RoleID  *uuid.UUID
	RoleKey string

	Detail    string
	IP        string
	UserAgent string
}

// MaxPage caps a page of history, the same way the other listings are capped.
const MaxPage = 100

// Reader is the organization-scoped history.
//
// Like orgs.Repository, every method takes the organization as its second
// parameter, so there is no way to read another tenant's history by accident.
type Reader interface {
	Events(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Event, error)
}

// PlatformReader is the installation-wide history, deliberately not scoped —
// which is why it is a separate interface behind a separate permission.
type PlatformReader interface {
	AllEvents(ctx context.Context, limit, offset int) ([]Event, error)
}

// Service reads the history back.
type Service struct {
	reader   Reader
	platform PlatformReader
}

func NewService(reader Reader, platform PlatformReader) *Service {
	return &Service{reader: reader, platform: platform}
}

// Events is one organization's history, newest first.
func (s *Service) Events(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Event, error) {
	limit, offset = clamp(limit, offset)

	return s.reader.Events(ctx, orgID, limit, offset)
}

// AllEvents is the installation's history, newest first.
func (s *Service) AllEvents(ctx context.Context, limit, offset int) ([]Event, error) {
	limit, offset = clamp(limit, offset)

	return s.platform.AllEvents(ctx, limit, offset)
}

func clamp(limit, offset int) (int, int) {
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}

	if offset < 0 {
		offset = 0
	}

	return limit, offset
}
