package seed

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

const (
	// VolumeAccounts is how many extra accounts the volume part creates.
	//
	// A hundred is chosen against orgs.MaxMemberPage, which is also a hundred: acme
	// ends up with more than one page of members, so paging is something a person
	// runs into by opening the screen rather than something only a test knows about.
	VolumeAccounts = 100

	// sameNameCount people share sameName, which is what makes the members
	// listing's (name, id) ordering worth having: sorting by name alone leaves
	// ties, and a page boundary inside a tie is where rows go missing.
	sameNameCount = 3
	sameName      = "Jan Kowalski"

	// suspendedCount of the crowd are suspended. A suspended member keeps its roles
	// and grants nothing, and a screen that renders that wrongly is one nobody
	// notices until somebody says they cannot get in.
	suspendedCount = 4
)

type volume struct{}

func (volume) Name() string { return "volume" }

func (v volume) Run(ctx context.Context, w *World) error {
	acme, err := w.ensureOrganization(ctx, OrgAcme, "Acme (seed)")
	if err != nil {
		return err
	}

	globex, err := w.ensureOrganization(ctx, OrgGlobex, "Globex (seed)")
	if err != nil {
		return err
	}

	member, err := w.role(ctx, acme.ID, authz.RoleMember)
	if err != nil {
		return err
	}

	viewer, err := w.role(ctx, acme.ID, authz.RoleViewer)
	if err != nil {
		return err
	}

	globexMember, err := w.role(ctx, globex.ID, authz.RoleMember)
	if err != nil {
		return err
	}

	for i := range VolumeAccounts {
		// The handle carries the index, so the hundredth account is findable by name
		// and a second run produces the same hundred people rather than another
		// hundred beside them.
		handle := fmt.Sprintf("crowd%03d", i+1)

		name := w.fakeName()
		if i < sameNameCount {
			name = sameName
		}

		account, err := w.ensureAccount(ctx, handle, name, w.fakeLocale())
		if err != nil {
			return err
		}

		role := member
		if i%3 == 0 {
			role = viewer
		}

		if err := w.ensureMember(ctx, acme.ID, account, role); err != nil {
			return err
		}

		// Every fifth one is in globex as well, so the small organization is not
		// uniform either and somebody appears in two member lists.
		if i%5 == 0 {
			if err := w.ensureMember(ctx, globex.ID, account, globexMember); err != nil {
				return err
			}
		}
	}

	return v.suspendSome(ctx, w, acme.ID)
}

// suspendSome suspends a few of the crowd through the real service method, so the
// rank rule and the last-owner guard both run and the audit log gets its entries.
func (volume) suspendSome(ctx context.Context, w *World, orgID uuid.UUID) error {
	owner, err := w.castAccount(ctx, "owner")
	if err != nil {
		return err
	}

	grant := w.ownerGrant(orgID, owner.ID)
	acting := w.actingAs(ctx, owner.ID)

	for i := 1; i <= suspendedCount; i++ {
		handle := fmt.Sprintf("crowd%03d", i)

		target, ok := w.accounts[handle]
		if !ok {
			continue
		}

		membership, err := w.Repo.MemberByUser(ctx, orgID, target.ID)
		if err != nil {
			return err
		}

		// Idempotent: a second run finds them already suspended and leaves the
		// audit log alone rather than adding an identical entry every time.
		if membership.Status == models.MembershipSuspended {
			continue
		}

		if err := w.Orgs.SetMemberStatus(acting, grant, membership.ID, models.MembershipSuspended); err != nil {
			return fmt.Errorf("suspend %s: %w", handle, err)
		}
	}

	return nil
}
