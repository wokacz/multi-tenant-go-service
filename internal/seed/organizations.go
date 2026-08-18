package seed

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The four fixed organizations, each one a shape the code has a rule about.
const (
	// OrgAcme is the busy one: the cast lives here and the volume part fills it
	// past one page, so pagination and its ordering are exercised by opening it.
	OrgAcme = "acme"

	// OrgGlobex is the small one, and the one invitations are addressed to.
	OrgGlobex = "globex"

	// OrgSolo has exactly one owner, so the last-owner refusal is reachable
	// without arranging anything first.
	OrgSolo = "solo"

	// OrgAbandoned loses its only owner in the states part, by deleting that
	// account. It is what ?without_owner=true is for, and it cannot be produced
	// through the API at all — only by an account going away.
	OrgAbandoned = "abandoned"

	// OrgEmpty is created and never filled, which is exactly what the platform
	// endpoint produces before anybody is appointed.
	OrgEmpty = "empty"
)

// The two names seed-abandoned wears. The second one is how the states part knows
// it has already done its work, and how somebody reading a list of organizations
// knows why that one has nobody in it.
const (
	abandonedName     = "Abandoned (seed)"
	abandonedDoneName = "Abandoned (seed, owner deleted)"
)

type organizations struct{}

func (organizations) Name() string { return "organizations" }

func (o organizations) Run(ctx context.Context, w *World) error {
	acme, err := w.ensureOrganization(ctx, OrgAcme, "Acme (seed)")
	if err != nil {
		return err
	}

	globex, err := w.ensureOrganization(ctx, OrgGlobex, "Globex (seed)")
	if err != nil {
		return err
	}

	solo, err := w.ensureOrganization(ctx, OrgSolo, "Solo (seed)")
	if err != nil {
		return err
	}

	if _, err := w.ensureOrganization(ctx, OrgAbandoned, abandonedName); err != nil {
		return err
	}

	if _, err := w.ensureOrganization(ctx, OrgEmpty, "Empty (seed)"); err != nil {
		return err
	}

	// Owners first: everything else in this part needs a grant, and a grant needs
	// somebody who could plausibly hold one.
	owners := map[string]string{
		OrgAcme:   "owner",
		OrgSolo:   "lastowner",
		OrgGlobex: "multiorg",
	}

	for slug, handle := range owners {
		account, err := w.castAccount(ctx, handle)
		if err != nil {
			return err
		}

		// PromoteToOwner rather than AddMember with the owner role: it is the
		// provisioning path, it is idempotent, and it is what bootstrap uses.
		err = w.Orgs.PromoteToOwner(w.actingAs(ctx, account.ID),
			w.orgs[Slug(slug)].ID, account.ID, false)
		if err != nil {
			return err
		}
	}

	// The installation administrator: owner of acme *and* platform_admin, because
	// somebody has to be able to see the platform screens.
	platform, err := w.castAccount(ctx, "platform")
	if err != nil {
		return err
	}

	if err := w.Orgs.PromoteToOwner(w.actingAs(ctx, platform.ID), acme.ID, platform.ID, true); err != nil {
		return err
	}

	if err := o.customRoles(ctx, w, acme.ID); err != nil {
		return err
	}

	_ = globex

	return o.memberships(ctx, w, acme, solo)
}

// customRoles gives acme the two roles that split members.invite from
// members.remove, which is the distinction A6 made and which no shipped role has.
func (organizations) customRoles(ctx context.Context, w *World, orgID uuid.UUID) error {
	for _, def := range customRoles {
		_, err := w.Repo.RoleByKey(ctx, orgID, def.Key)
		if err == nil {
			continue
		}

		if !errors.Is(err, orgs.ErrNotFound) {
			return err
		}

		role := &models.Role{Key: def.Key, Name: def.Name, Description: def.Description}
		if _, err := w.Repo.CreateRole(ctx, orgID, role, def.Permissions); err != nil {
			return fmt.Errorf("custom role %s: %w", def.Key, err)
		}
	}

	return nil
}

// memberships puts the rest of the cast where the documentation says they are.
func (organizations) memberships(ctx context.Context, w *World, acme, solo *models.Organization) error {
	shipped := map[authz.RoleKey]uuid.UUID{}

	for _, key := range []authz.RoleKey{authz.RoleAdmin, authz.RoleMember, authz.RoleViewer} {
		id, err := w.role(ctx, acme.ID, key)
		if err != nil {
			return err
		}

		shipped[key] = id
	}

	inviter, err := w.Repo.RoleByKey(ctx, acme.ID, "inviter")
	if err != nil {
		return err
	}

	remover, err := w.Repo.RoleByKey(ctx, acme.ID, "remover")
	if err != nil {
		return err
	}

	inAcme := map[string]uuid.UUID{
		"admin":     shipped[authz.RoleAdmin],
		"member":    shipped[authz.RoleMember],
		"viewer":    shipped[authz.RoleViewer],
		"suspended": shipped[authz.RoleMember],
		"twofactor": shipped[authz.RoleMember],
		"changing":  shipped[authz.RoleMember],
		"inviter":   inviter.ID,
		"remover":   remover.ID,
		"invited":   shipped[authz.RoleViewer],
	}

	for handle, roleID := range inAcme {
		account, err := w.castAccount(ctx, handle)
		if err != nil {
			return err
		}

		if err := w.ensureMember(ctx, acme.ID, account, roleID); err != nil {
			return err
		}
	}

	// multiorg holds a different role in each of three organizations, which is what
	// makes the permission snapshot worth reading.
	multiorg, err := w.castAccount(ctx, "multiorg")
	if err != nil {
		return err
	}

	acmeMember, err := w.role(ctx, acme.ID, authz.RoleMember)
	if err != nil {
		return err
	}

	if err := w.ensureMember(ctx, acme.ID, multiorg, acmeMember); err != nil {
		return err
	}

	soloViewer, err := w.role(ctx, solo.ID, authz.RoleViewer)
	if err != nil {
		return err
	}

	if err := w.ensureMember(ctx, solo.ID, multiorg, soloViewer); err != nil {
		return err
	}

	// nowhere joins acme so that the states part has a membership to leave. An
	// account that never joined anything would look the same afterwards but would
	// not have exercised leaving.
	nowhere, err := w.castAccount(ctx, "nowhere")
	if err != nil {
		return err
	}

	if err := w.ensureMember(ctx, acme.ID, nowhere, acmeMember); err != nil {
		return err
	}

	return nil
}
