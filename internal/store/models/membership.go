package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MembershipStatus enumerates where a person stands in an organization.
type MembershipStatus string

const (
	// MembershipInvited has been asked to join and has not accepted. The row
	// exists so the invitation can be listed and withdrawn; it confers nothing.
	// UserID is nil until they accept, so listing invitations cannot tell a
	// registered address from an unknown one.
	MembershipInvited MembershipStatus = "invited"

	MembershipActive MembershipStatus = "active"

	// MembershipSuspended keeps the row — and therefore the role assignments —
	// so that reinstating someone restores exactly what they had. Suspension
	// that deleted the assignments would silently become "remove and re-add".
	MembershipSuspended MembershipStatus = "suspended"
)

func (s MembershipStatus) Valid() bool {
	switch s {
	case MembershipInvited, MembershipActive, MembershipSuspended:
		return true
	default:
		return false
	}
}

// GrantsPermissions is the rule that only an active membership confers anything.
//
// It lives on the type rather than as an `== MembershipActive` comparison at
// each call site, because those comparisons are what drift: one of them gets
// written as `!= MembershipSuspended` and an invitation quietly becomes a
// membership.
func (s MembershipStatus) GrantsPermissions() bool {
	return s == MembershipActive
}

type Membership struct {
	Model

	// UserID is nil while the invitation is outstanding. Postgres unique
	// indexes treat two NULLs as distinct, which is what lets several people
	// be invited to one organization at once; uniqueness for that case is
	// idx_membership_org_email.
	UserID *uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_membership_user_org,priority:1"`
	User   *User      `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	OrganizationID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_membership_user_org,priority:2;uniqueIndex:idx_membership_org_email,priority:1"`
	Organization   *Organization `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	// Email is the invitation's identity. It is denormalised onto the row so
	// an outstanding invite can be unique per organization without first
	// looking the address up in users — that lookup is the enumeration
	// oracle this column exists to close.
	Email string `gorm:"size:255;not null;uniqueIndex:idx_membership_org_email,priority:2"`

	Status MembershipStatus `gorm:"size:20;not null;check:status IN ('invited','active','suspended')"`

	// InvitedBy is a bare column rather than a relation: it points at a user who
	// may later be deleted, and the invitation record should outlive them.
	InvitedBy *uuid.UUID `gorm:"type:uuid"`
	JoinedAt  *time.Time

	Roles []MembershipRole `gorm:"constraint:OnDelete:CASCADE"`
}

// AccountID is the user behind this membership, or uuid.Nil while invited.
func (m Membership) AccountID() uuid.UUID {
	if m.UserID == nil {
		return uuid.Nil
	}

	return *m.UserID
}

// BeforeSave rejects an unknown status in Go, so callers get a useful error
// rather than a constraint violation from Postgres.
func (m *Membership) BeforeSave(_ *gorm.DB) error {
	if !m.Status.Valid() {
		return fmt.Errorf("models: invalid membership status %q", m.Status)
	}

	if strings.TrimSpace(m.Email) == "" {
		return fmt.Errorf("models: membership email is empty")
	}

	if m.Status.GrantsPermissions() && m.UserID == nil {
		return fmt.Errorf("models: an active membership needs an account")
	}

	return nil
}

// Activate accepts an invitation. JoinedAt is only stamped the first time, so
// reinstating a suspended member does not rewrite when they actually joined.
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
