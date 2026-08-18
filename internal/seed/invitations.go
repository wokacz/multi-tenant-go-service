package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
)

type invitations struct{}

func (invitations) Name() string { return "invitations" }

// Run leaves globex with three kinds of outstanding offer.
//
// The tokens are printed, not stored: an invitation exists once in the message that
// carried it, and the seeder is the only place that ever sees one. Anybody wanting
// to accept an invitation by hand needs the token from the log.
func (in invitations) Run(ctx context.Context, w *World) error {
	globex, err := w.ensureOrganization(ctx, OrgGlobex, "Globex (seed)")
	if err != nil {
		return err
	}

	owner, err := w.castAccount(ctx, "multiorg")
	if err != nil {
		return err
	}

	grant := w.ownerGrant(globex.ID, owner.ID)
	acting := w.actingAs(ctx, owner.ID)

	viewer, err := w.role(ctx, globex.ID, authz.RoleViewer)
	if err != nil {
		return err
	}

	// One to an account that exists, which is the case where accepting works
	// immediately — and the case the address-match rule constrains.
	invited, err := w.castAccount(ctx, "invited")
	if err != nil {
		return err
	}

	if err := in.offer(acting, w, grant, invited.Email, viewer, false); err != nil {
		return err
	}

	// One to an address with no account behind it, which is the ordinary case and
	// the one that must look identical from outside — an invitation that revealed
	// whether the address is registered would be an oracle for who has an account.
	if err := in.offer(acting, w, grant, Email("stranger"), viewer, false); err != nil {
		return err
	}

	// One that has already expired, so the 410 and the "ask for another" path are
	// reachable without waiting a week.
	return in.offer(acting, w, grant, Email("forgotten"), viewer, true)
}

// offer sends one invitation, optionally pushing its expiry into the past.
//
// Expiring one goes through ReissueInvitation, which takes the new expiry as a
// parameter. That is not a trick: reissuing is the one operation allowed to move an
// invitation's clock, and using it here means the seeder cannot produce an expiry
// the application could not.
func (invitations) offer(
	ctx context.Context,
	w *World,
	grant *authz.Grant,
	email string,
	roleID uuid.UUID,
	expired bool,
) error {
	invitation, token, err := w.Orgs.Invite(ctx, grant, email, []uuid.UUID{roleID})
	if err != nil {
		// Already invited, or already a member: both mean the state this part wants
		// is the state that is there.
		if errors.Is(err, orgs.ErrAlreadyMember) {
			return nil
		}

		return fmt.Errorf("invite %s: %w", email, err)
	}

	w.Log.Info("invitation", slog.String("email", email), slog.String("token", token))

	if !expired {
		return nil
	}

	_, err = w.Repo.ReissueInvitation(ctx, invitation.Organization.ID, invitation.ID,
		orgs.HashInvitationToken(token), w.Now.Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("expire the invitation for %s: %w", email, err)
	}

	return nil
}
