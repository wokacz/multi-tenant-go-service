package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

type states struct{}

func (states) Name() string { return "states" }

// Run puts the cast into the states that cannot be described by a membership row.
//
// It runs last because each of these acts on something an earlier part built, and
// one of them destroys an account on purpose.
func (st states) Run(ctx context.Context, w *World) error {
	if err := st.suspendCastMember(ctx, w); err != nil {
		return err
	}

	if err := st.enableTwoFactor(ctx, w); err != nil {
		return err
	}

	if err := st.pendingEmailChange(ctx, w); err != nil {
		return err
	}

	if err := st.leaveEverything(ctx, w); err != nil {
		return err
	}

	return st.abandonAnOrganization(ctx, w)
}

// suspendCastMember suspends the account the documentation calls suspended.
func (states) suspendCastMember(ctx context.Context, w *World) error {
	acme, err := w.ensureOrganization(ctx, OrgAcme, "Acme (seed)")
	if err != nil {
		return err
	}

	owner, err := w.castAccount(ctx, "owner")
	if err != nil {
		return err
	}

	target, err := w.castAccount(ctx, "suspended")
	if err != nil {
		return err
	}

	membership, err := w.Repo.MemberByUser(ctx, acme.ID, target.ID)
	if err != nil {
		return err
	}

	if !membership.Status.GrantsPermissions() {
		return nil
	}

	return w.Orgs.SetMemberStatus(w.actingAs(ctx, owner.ID),
		w.ownerGrant(acme.ID, owner.ID), membership.ID, models.MembershipSuspended)
}

// enableTwoFactor turns the second factor on for the account named after it, which
// makes signing in as them answer 202 and email a code.
func (states) enableTwoFactor(ctx context.Context, w *World) error {
	account, err := w.castAccount(ctx, "twofactor")
	if err != nil {
		return err
	}

	if account.TwoFactorEnabled {
		return nil
	}

	// The password is the authorization, exactly as it is over HTTP: this is not a
	// setting an operator flips on somebody's behalf, and the seeder knows the
	// password only because it chose it.
	return w.Users.SetTwoFactor(ctx, account.ID, Password, true, user.SignInContext{
		IP:        "127.0.0.1",
		UserAgent: "seed",
	})
}

// pendingEmailChange leaves an unconfirmed address change, so the confirmation
// screen has something to confirm and the code path that keeps the old address
// until it is proved is visible in the database.
func (states) pendingEmailChange(ctx context.Context, w *World) error {
	account, err := w.castAccount(ctx, "changing")
	if err != nil {
		return err
	}

	if _, err := w.Users.PendingEmailChange(ctx, account.ID); err == nil {
		return nil
	}

	code, err := w.Users.BeginEmailChange(ctx, account.ID, Email("changed"), Password)
	if err != nil {
		if errors.Is(err, user.ErrSameEmail) {
			return nil
		}

		return err
	}

	w.Log.Info("pending email change",
		slog.String("account", account.Email),
		slog.String("new_email", Email("changed")),
		slog.String("code", code))

	return nil
}

// leaveEverything walks one account out of every organization it is in, through the
// self-service endpoint's own service method. What is left is an account that
// belongs to nothing — which is a state a client has to render, and the one a
// leaver is in a second after leaving.
func (states) leaveEverything(ctx context.Context, w *World) error {
	account, err := w.castAccount(ctx, "nowhere")
	if err != nil {
		return err
	}

	mine, err := w.Orgs.Mine(ctx, account.ID)
	if err != nil {
		return err
	}

	for _, membership := range mine {
		err := w.Orgs.Leave(w.actingAs(ctx, account.ID), account.ID, membership.ID)
		if err != nil && !errors.Is(err, orgs.ErrLastOwner) {
			return fmt.Errorf("leave %s: %w", membership.Organization.Slug, err)
		}
	}

	return nil
}

// abandonAnOrganization gives seed-abandoned an owner and then deletes that
// account.
//
// This is the one state in the whole seeder the API cannot produce, and the reason
// the ownerless listing exists: the membership row outlives the person, still holding
// owner, and stops counting everywhere it matters. An installation administrator
// finds it with ?without_owner=true and fixes it by appointing somebody.
//
// The whole story is here rather than split with the organizations part, because it
// only makes sense as one act: create, promote, delete. Doing the first two elsewhere
// meant a second run created a *second* owner to delete, and the ghost memberships
// piled up one per run.
//
// Being done is recorded in the organization's display name, which is also the only
// signal available: through the domain interfaces an organization with a ghost
// membership is indistinguishable from an empty one — every read filters the ghost
// out, which is exactly the rule that makes the state worth seeding. The name is
// visible in any UI, which beats a marker only this code can read.
func (states) abandonAnOrganization(ctx context.Context, w *World) error {
	org, err := w.ensureOrganization(ctx, OrgAbandoned, abandonedName)
	if err != nil {
		return err
	}

	if org.Name == abandonedDoneName {
		return nil
	}

	doomed, err := w.ensureAccount(ctx, "abandonedowner", "Anna Abandoned", "pl")
	if err != nil {
		return err
	}

	acting := w.actingAs(ctx, doomed.ID)

	if err := w.Orgs.PromoteToOwner(acting, org.ID, doomed.ID, false); err != nil {
		return err
	}

	if err := w.Users.Delete(ctx, doomed.ID); err != nil {
		return err
	}

	w.forget("abandonedowner")

	// Renamed last: if anything above failed, the next run tries again rather than
	// believing a half-finished job.
	if err := w.Repo.UpdateOrganization(ctx, org.ID, abandonedDoneName); err != nil {
		return err
	}

	// The register holds the old name, and a later part reading it would see an
	// organization that is not the one in the database.
	delete(w.orgs, Slug(OrgAbandoned))

	w.Log.Info("abandoned an organization",
		slog.String("organization", org.Slug),
		slog.String("deleted_owner", doomed.Email))

	return nil
}
