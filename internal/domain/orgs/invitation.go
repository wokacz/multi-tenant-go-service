package orgs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// invitationTokenBytes is the size of the secret in the message.
//
// 32 bytes is far past anything guessable, which is what lets the hash be a plain
// SHA-256 with no pepper: there is no dictionary to run against it. The six-digit
// codes elsewhere in the product need a keyed hash for exactly the opposite
// reason.
const invitationTokenBytes = 32

// InvitationTTL is how long an offer stays open.
//
// An invitation that never expires is a credential to an organization sitting in
// somebody's inbox indefinitely — and the old model, where the address was the
// identity, had no expiry at all. Seven days is long enough to survive a holiday
// and short enough that a forwarded mailbox does not stay dangerous for ever.
const InvitationTTL = 7 * 24 * time.Hour

// Invite records an invitation and returns the token to put in the message.
//
// The token is returned and never stored: what goes into the table is its hash,
// so somebody who can read the table cannot accept on the invitee's behalf. This
// is the only moment the token exists in this process.
func (s *Service) Invite(
	ctx context.Context,
	grant *authz.Grant,
	email string,
	roleIDs []uuid.UUID,
) (*Invitation, string, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, "", ErrInvalidEmail
	}

	if err := s.ensureRolesAreGrantable(ctx, grant, roleIDs); err != nil {
		return nil, "", err
	}

	token, err := newInvitationToken()
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()

	invitation, err := s.repo.InviteMember(ctx, grant.OrganizationID(), email,
		HashInvitationToken(token), roleIDs, grant.Actor(), now.Add(InvitationTTL), now)
	if err != nil {
		return nil, "", err
	}

	return invitation, token, nil
}

// AcceptInvitation turns a token into a membership.
//
// Two things have to hold, and they answer different questions. The token proves
// the caller received the message — that is what replaced "whoever registers this
// address first". The address then has to match the account accepting: that keeps
// the offer pointed at the person it was meant for rather than at whoever the
// message was forwarded to.
//
// The roles are read from the invitation inside the repository, never taken from
// the caller. Somebody accepting an offer must not get to choose what they are
// accepting.
func (s *Service) AcceptInvitation(ctx context.Context, userID uuid.UUID, accountEmail, token string) error {
	invitation, err := s.pendingInvitation(ctx, token)
	if err != nil {
		return err
	}

	if invitation.Email != normalizeEmail(accountEmail) {
		return ErrInvitationAddressMismatch
	}

	return s.dir.AcceptInvitation(ctx, invitation.ID, userID, time.Now().UTC())
}

// DeclineInvitation withdraws an offer. Holding the token is the authorization:
// whoever can read the mailbox is entitled to refuse on its behalf, and requiring
// an account as well would mean an invitation to somebody who never registers can
// only be cleaned up by the organization.
func (s *Service) DeclineInvitation(ctx context.Context, token string) error {
	invitation, err := s.pendingInvitation(ctx, token)
	if err != nil {
		return err
	}

	return s.dir.DeclineInvitation(ctx, invitation.ID)
}

// MyInvitations lists what has been offered to one address.
//
// It carries no token, so it cannot be used to accept. It exists so an
// invitation does not simply fail to appear anywhere in the product: somebody who
// deleted the message can at least see that an offer is open and ask for it again.
func (s *Service) MyInvitations(ctx context.Context, accountEmail string) ([]Invitation, error) {
	return s.dir.InvitationsForEmail(ctx, normalizeEmail(accountEmail), time.Now().UTC())
}

// pendingInvitation resolves a token, separating "no such invitation" from "it
// has expired".
//
// The repository answers ErrNotFound for both, because it filters on the clock in
// the same query. Telling them apart needs a second look without that filter,
// which is worth one query: "ask for another invitation" and "check the link" are
// different things for the person holding the message to do.
func (s *Service) pendingInvitation(ctx context.Context, token string) (*Invitation, error) {
	hash := HashInvitationToken(token)
	now := time.Now().UTC()

	invitation, err := s.dir.InvitationByToken(ctx, hash, now)
	if err == nil {
		return invitation, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Zero time matches any expiry, so this finds the row the clock hid.
	expired, lookupErr := s.dir.InvitationByToken(ctx, hash, time.Time{})
	if lookupErr == nil && expired.ExpiresAt.Before(now) {
		return nil, ErrInvitationExpired
	}

	return nil, ErrNotFound
}

// HashInvitationToken is the one-way transform between the secret in the message
// and the value in the table. It is exported because the store's tests need to
// build a row that a given token unlocks.
func HashInvitationToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func newInvitationToken() (string, error) {
	buf := make([]byte, invitationTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("orgs: generate invitation token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Invitations lists what an organization has outstanding.
//
// It is the administrator's half of the invitation lifecycle. While an invitation
// was a membership row it appeared in the members list, so removing it from there
// without putting it anywhere else would have left an offer nobody could see or
// take back.
func (s *Service) Invitations(ctx context.Context, grant *authz.Grant) ([]Invitation, error) {
	return s.repo.InvitationsForOrganization(ctx, grant.OrganizationID(), time.Now().UTC())
}

// WithdrawInvitation takes back an offer the organization made.
//
// Declining and withdrawing are separate operations on purpose, and not because
// they do different things to the row. They are authorized by different facts: the
// invitee holds the token, the organization holds members.remove. Folding them into
// one endpoint would mean one of the two authorizations standing in for the other.
func (s *Service) WithdrawInvitation(ctx context.Context, grant *authz.Grant, invitationID uuid.UUID) error {
	return s.repo.WithdrawInvitation(ctx, grant.OrganizationID(), invitationID)
}

// Reissue replaces an outstanding invitation's token and pushes its expiry out,
// returning the new token to mail.
//
// The old token stops working, which is the point: "resend" that mailed the same
// secret again would mean a leaked link stays valid for another week, and one that
// created a second invitation would collide with the first on (organization, email).
func (s *Service) Reissue(
	ctx context.Context,
	grant *authz.Grant,
	invitationID uuid.UUID,
) (*Invitation, string, error) {
	token, err := newInvitationToken()
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()

	invitation, err := s.repo.ReissueInvitation(ctx, grant.OrganizationID(), invitationID,
		HashInvitationToken(token), now.Add(InvitationTTL))
	if err != nil {
		return nil, "", err
	}

	return invitation, token, nil
}
