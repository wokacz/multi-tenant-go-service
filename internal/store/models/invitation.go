package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Invitation is an offer of membership, identified by a secret.
//
// It used to be a membership row with status='invited' and no account behind it.
// That made the *address* the identity of the invitation, and the address is not a
// secret: whoever registered an invited address first inherited the offer and the
// roles attached to it, in an organization they had never been part of. A token
// moves the proof from "I claim this address" to "I can read this mailbox".
//
// Being its own table also gives the invitation its own lifecycle. Expiry,
// reissuing and withdrawal are properties of an offer, not of a membership, and
// hanging them off the memberships table meant every one of them had to be
// expressed as a membership that is not really a membership.
type Invitation struct {
	Model

	OrganizationID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_invitation_org_email,priority:1"`
	Organization   *Organization `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	// Email is where the offer was sent. It is unique per organization so the
	// same address cannot have two outstanding offers to the same place, and it
	// is still compared at acceptance — the token proves the mailbox, the address
	// says which mailbox was meant.
	Email string `gorm:"size:255;not null;uniqueIndex:idx_invitation_org_email,priority:2"`

	// TokenHash is the only copy of the token this side keeps. Plain SHA-256
	// rather than an HMAC with a pepper, and that is a deliberate difference from
	// the six-digit codes: those have little entropy and need a secret to make
	// an offline guess expensive. This is 32 random bytes, so there is nothing to
	// guess and a keyed hash would only add a secret to lose. Device fingerprints
	// are hashed the same way for the same reason.
	TokenHash string `gorm:"size:64;not null;uniqueIndex"`

	// InvitedBy is a bare column rather than a relation: it points at a user who
	// may later be deleted, and the record of who invited whom should outlive
	// them.
	InvitedBy *uuid.UUID `gorm:"type:uuid"`

	// ExpiresAt is what stops an offer from being valid for ever. An invitation
	// that never expires is a credential to an organization sitting in somebody's
	// inbox indefinitely.
	ExpiresAt time.Time `gorm:"not null;index"`

	// AcceptedAt marks it spent. The row is kept rather than deleted so the
	// history of who was offered what survives the membership it produced.
	AcceptedAt *time.Time

	Roles []InvitationRole `gorm:"constraint:OnDelete:CASCADE"`
}

// BeforeSave keeps the two things that make a row meaningful from being empty.
func (i *Invitation) BeforeSave(_ *gorm.DB) error {
	if strings.TrimSpace(i.Email) == "" {
		return fmt.Errorf("models: invitation email is empty")
	}

	if strings.TrimSpace(i.TokenHash) == "" {
		return fmt.Errorf("models: invitation token hash is empty")
	}

	return nil
}

// Pending reports whether the invitation can still be taken up at the given time.
func (i Invitation) Pending(now time.Time) bool {
	return i.AcceptedAt == nil && i.ExpiresAt.After(now)
}

// InvitationRole is a role the invitation grants once accepted. It mirrors
// MembershipRole, because the roles have to survive from the offer to the
// membership without being re-chosen at acceptance — the person accepting must
// not be able to pick what they are accepting.
type InvitationRole struct {
	Model

	InvitationID uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_invitation_role,priority:1"`
	Invitation   *Invitation `json:"-" gorm:"constraint:OnDelete:CASCADE"`

	RoleID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_invitation_role,priority:2"`
	Role   *Role     `json:"-" gorm:"constraint:OnDelete:CASCADE"`
}
