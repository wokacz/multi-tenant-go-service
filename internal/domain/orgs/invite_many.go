package orgs

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// MaxInvitesPerRequest caps one batch.
//
// A cap is needed because the work is per address — a token, a row, a message —
// and one request costs one token from the rate limiter however long the list is.
// Fifty is enough for a team onboarding in one go, which is the case that made the
// shared registration budget fail at five.
const MaxInvitesPerRequest = 50

// ErrInvalidInvitationBatch refuses a list that is empty, over the cap, or names
// the same address twice.
//
// One error for three shapes because an HTTP caller does not normally see it: the
// schema carries minItems, maxItems and uniqueItems, so huma refuses those with a
// message naming the exact field. This is the backstop for a caller that is not a
// request, and for the one case a schema cannot see — Ada@example.com and
// ada@example.com are the same address, and only normalisation knows it.
var ErrInvalidInvitationBatch = errors.New("orgs: the invitation batch is empty, too long, or repeats an address")

// InviteOutcome is what happened to one address in a batch.
//
// Each address gets its own answer instead of the whole batch failing on the first
// refusal. An administrator pasting a list of twelve colleagues, two of whom are
// already members, wants the ten invitations sent — all-or-nothing would make them
// find the two by bisection.
type InviteOutcome struct {
	Email string

	// Invitation and Token are set when one was created. The token exists here and
	// in the message, and nowhere else — the caller mails it and drops it.
	Invitation *Invitation
	Token      string

	// Err is why this address got nothing. ErrAlreadyMember is the expected one;
	// anything else aborts the batch rather than landing here.
	Err error
}

// InviteMany invites a list of addresses to one organization with one role set.
//
// The roles are checked once, before anything is written: they are the same for
// every address, so a caller trying to grant more than they hold is refused as a
// request rather than as fifty identical refusals.
//
// There is no transaction around the batch, and that is the point of the outcome
// list. Rolling back nine invitations because the tenth address is already a member
// would throw away work the caller asked for and would have to be undone by hand.
// Each invitation is its own row, its own token and its own audit entry, exactly as
// if it had been sent one at a time — which is also why this reuses Invite instead
// of a bulk insert that would have to reproduce all three.
func (s *Service) InviteMany(
	ctx context.Context,
	grant *authz.Grant,
	emails []string,
	roleIDs []uuid.UUID,
) ([]InviteOutcome, error) {
	if len(emails) == 0 || len(emails) > MaxInvitesPerRequest {
		return nil, ErrInvalidInvitationBatch
	}

	seen := make(map[string]struct{}, len(emails))

	for _, email := range emails {
		normalized := normalizeEmail(email)
		if normalized == "" {
			return nil, ErrInvalidEmail
		}

		if _, repeated := seen[normalized]; repeated {
			return nil, ErrInvalidInvitationBatch
		}

		seen[normalized] = struct{}{}
	}

	if err := s.ensureRolesAreGrantable(ctx, grant, roleIDs); err != nil {
		return nil, err
	}

	outcomes := make([]InviteOutcome, 0, len(emails))

	for _, email := range emails {
		invitation, token, err := s.Invite(ctx, grant, email, roleIDs)

		switch {
		case err == nil:
			outcomes = append(outcomes, InviteOutcome{
				Email: invitation.Email, Invitation: invitation, Token: token,
			})

		case errors.Is(err, ErrAlreadyMember):
			// The one refusal that is about this address rather than about the
			// request. Everything else — a broken connection, a role that vanished
			// mid-batch — is a fact about the whole call and is reported as one.
			outcomes = append(outcomes, InviteOutcome{
				Email: normalizeEmail(email), Err: err,
			})

		default:
			return nil, err
		}
	}

	return outcomes, nil
}
