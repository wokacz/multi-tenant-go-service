package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MembershipStatus enumerates where a person stands in an organization.
type MembershipStatus string

// There is deliberately no "invited" status.
//
// An offer of membership is not a membership, and while it was one, every read
// had to remember that some rows with no account behind them were not really
// members — which is exactly the kind of thing a query eventually forgets.
// Invitations live in their own table.
const (
	MembershipActive MembershipStatus = "active"

	// MembershipSuspended keeps the row — and therefore the role assignments —
	// so that reinstating someone restores exactly what they had. Suspension
	// that deleted the assignments would silently become "remove and re-add".
	MembershipSuspended MembershipStatus = "suspended"
)

func (s MembershipStatus) Valid() bool {
	switch s {
	case MembershipActive, MembershipSuspended:
		return true
	default:
		return false
	}
}

// GrantsPermissions is the rule that only an active membership confers anything.
//
// It lives on the type rather than as an `== MembershipActive` comparison at
// each call site, because those comparisons are what drift: one of them gets
// written as `!= MembershipSuspended` and the next status added to the enum
// quietly grants everything.
func (s MembershipStatus) GrantsPermissions() bool {
	return s == MembershipActive
}

type Membership struct {
	Model

	// UserID is NOT NULL again. It was nullable while an invitation was a
	// membership, and that had two costs: every query had to cope with a member
	// who was not a person, and the unique index on (user_id, organization_id)
	// could not do its job, because Postgres treats two NULLs as distinct.
	UserID uuid.UUID
	User   *User `json:"-"`

	OrganizationID uuid.UUID
	Organization   *Organization `json:"-"`

	Status MembershipStatus

	// InvitedBy is a bare column rather than a relation: it points at a user who
	// may later be deleted, and the record of who brought somebody in should
	// outlive them. It is copied from the invitation when one is accepted.
	InvitedBy *uuid.UUID
	JoinedAt  *time.Time

	Roles []MembershipRole `json:"-"`
}

// Validate rejects an unknown status in Go, so callers get a useful error
// rather than a constraint violation from Postgres.
func (m *Membership) Validate() error {
	if !m.Status.Valid() {
		return fmt.Errorf("models: invalid membership status %q", m.Status)
	}

	if m.UserID == uuid.Nil {
		return fmt.Errorf("models: membership has no account")
	}

	return nil
}

// Activate marks the membership active. JoinedAt is only stamped the first time,
// so reinstating a suspended member does not rewrite when they actually joined.
func (m *Membership) Activate(at time.Time) {
	m.Status = MembershipActive

	if m.JoinedAt == nil {
		joined := at.UTC()
		m.JoinedAt = &joined
	}
}

func (m *Membership) Suspend() {
	m.Status = MembershipSuspended
}

func (m *Membership) IsActive() bool {
	return m.Status.GrantsPermissions()
}
