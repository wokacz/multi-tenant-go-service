package repositories

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/invitation"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/invitationrole"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membership"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/organization"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/role"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

func (r *Orgs) InviteMember(
	ctx context.Context,
	orgID uuid.UUID,
	email, tokenHash string,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	expiresAt, at time.Time,
) (*orgs.Invitation, error) {
	var invitationID uuid.UUID

	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// An address that is already a member gets the same answer as one that
		// already has an invitation. The caller can act on either the same way, and
		// the two do not need telling apart badly enough to justify a second code.
		//
		// HasUserWith goes through the user interceptor, so a deleted account does
		// not occupy the address — the same predicate the GORM join wrote as
		// "u.deleted_at IS NULL".
		n, err := tx.Membership.Query().
			Where(
				membership.OrganizationID(orgID),
				membership.HasUserWith(entuser.Email(email)),
			).
			Count(ctx)
		if err != nil {
			return err
		}

		if n > 0 {
			return orgs.ErrAlreadyMember
		}

		create := tx.Invitation.Create().
			SetOrganizationID(orgID).
			SetEmail(email).
			SetTokenHash(tokenHash).
			SetExpiresAt(expiresAt.UTC()).
			SetNillableInvitedBy(invitedByPtr(invitedBy))
		if !at.IsZero() {
			create = create.SetCreatedAt(at.UTC())
		}

		created, err := create.Save(ctx)
		if err != nil {
			if isUniqueViolation(err) {
				return orgs.ErrAlreadyMember
			}

			return err
		}

		invitationID = created.ID

		if err := assignInvitationRoles(ctx, tx, orgID, created.ID, roleIDs); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionMemberInvited,
			Detail:         email,
		})
	})
	if err != nil {
		return nil, translateOrgError("invite member", err)
	}

	return r.invitation(ctx, invitationID)
}

func invitedByPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}

	return &id
}

// assignInvitationRoles is assignRoles for an invitation: the same filtered count
// that refuses a role id belonging to another organization, because an invitation
// is scoped exactly like the membership it will become.
func assignInvitationRoles(ctx context.Context, tx *ent.Tx, orgID, invitationID uuid.UUID, roleIDs []uuid.UUID) error {
	unique, err := ownedRoleIDs(ctx, tx, orgID, roleIDs)
	if err != nil {
		return err
	}

	if len(unique) == 0 {
		return nil
	}

	creates := make([]*ent.InvitationRoleCreate, 0, len(unique))
	for _, roleID := range unique {
		creates = append(creates, tx.InvitationRole.Create().
			SetInvitationID(invitationID).
			SetRoleID(roleID))
	}

	return tx.InvitationRole.CreateBulk(creates...).Exec(ctx)
}

func ownedRoleIDs(ctx context.Context, tx *ent.Tx, orgID uuid.UUID, roleIDs []uuid.UUID) ([]uuid.UUID, error) {
	unique := uniqueIDs(roleIDs)
	if len(unique) == 0 {
		return unique, nil
	}

	owned, err := tx.Role.Query().
		Where(role.OrganizationID(orgID), role.IDIn(unique...)).
		Count(ctx)
	if err != nil {
		return nil, err
	}

	if owned != len(unique) {
		return nil, orgs.ErrNotFound
	}

	return unique, nil
}

func (r *Orgs) InvitationByToken(ctx context.Context, tokenHash string, now time.Time) (*orgs.Invitation, error) {
	query := r.db.Ent().Invitation.Query().
		Where(
			invitation.TokenHash(tokenHash),
			invitation.AcceptedAtIsNil(),
			// Live organizations only: the interceptor on Organization hides retired
			// rows, so this is the JOIN … AND o.deleted_at IS NULL from the GORM
			// version, without writing the predicate out.
			invitation.HasOrganization(),
		)

	// A zero time means "ignore the clock", which is how the service tells an
	// expired invitation apart from one that never existed.
	if !now.IsZero() {
		query = query.Where(invitation.ExpiresAtGT(now))
	}

	row, err := query.First(ctx)
	if err != nil {
		return nil, translateOrgError("invitation by token", err)
	}

	return r.invitation(ctx, row.ID)
}

func (r *Orgs) InvitationsForEmail(ctx context.Context, email string, now time.Time) ([]orgs.Invitation, error) {
	rows, err := r.db.Ent().Invitation.Query().
		Where(
			invitation.Email(email),
			invitation.AcceptedAtIsNil(),
			invitation.ExpiresAtGT(now),
			invitation.HasOrganization(),
		).
		Order(ent.Desc(invitation.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: invitations for email: %w", err)
	}

	out := make([]orgs.Invitation, 0, len(rows))
	for _, row := range rows {
		view, err := r.invitation(ctx, row.ID)
		if err != nil {
			return nil, err
		}

		out = append(out, *view)
	}

	return out, nil
}

func (r *Orgs) AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID, at time.Time) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// Locked, so two clients racing the same link cannot both create a
		// membership from it.
		inv, err := tx.Invitation.Query().
			Where(invitation.ID(invitationID), invitation.AcceptedAtIsNil()).
			ForUpdate().
			Only(ctx)
		if err != nil {
			return err
		}

		// The organization has to still exist, checked here rather than only in
		// InvitationByToken. That lookup does filter deleted organizations, so the
		// service path cannot reach this with a dead one — but it is a separate
		// statement from this transaction, and an organization deleted in between
		// would leave an active membership in something that no longer exists.
		live, err := tx.Organization.Query().
			Where(organization.ID(inv.OrganizationID)).
			Count(ctx)
		if err != nil {
			return err
		}

		if live == 0 {
			return orgs.ErrNotFound
		}

		roleRows, err := tx.InvitationRole.Query().
			Where(invitationrole.InvitationID(invitationID)).
			All(ctx)
		if err != nil {
			return err
		}

		roleIDs := make([]uuid.UUID, 0, len(roleRows))
		for _, row := range roleRows {
			roleIDs = append(roleIDs, row.RoleID)
		}

		m := &models.Membership{
			OrganizationID: inv.OrganizationID,
			UserID:         userID,
			InvitedBy:      inv.InvitedBy,
		}
		m.Activate(at)

		created, err := tx.Membership.Create().
			SetOrganizationID(m.OrganizationID).
			SetUserID(m.UserID).
			SetStatus(membership.Status(m.Status)).
			SetNillableInvitedBy(m.InvitedBy).
			SetNillableJoinedAt(m.JoinedAt).
			Save(ctx)
		if err != nil {
			if isUniqueViolation(err) {
				return orgs.ErrAlreadyMember
			}

			return err
		}

		if err := assignMembershipRoles(ctx, tx, inv.OrganizationID, created.ID, roleIDs); err != nil {
			return err
		}

		if err := tx.Invitation.UpdateOneID(inv.ID).SetAcceptedAt(at.UTC()).Exec(ctx); err != nil {
			return err
		}

		subject := userID

		return recordEnt(ctx, tx, &models.AuthzEvent{
			OrganizationID: &inv.OrganizationID,
			SubjectID:      &subject,
			Action:         models.ActionMemberAccepted,
		})
	})

	return translateOrgError("accept invitation", err)
}

func assignMembershipRoles(ctx context.Context, tx *ent.Tx, orgID, membershipID uuid.UUID, roleIDs []uuid.UUID) error {
	unique, err := ownedRoleIDs(ctx, tx, orgID, roleIDs)
	if err != nil {
		return err
	}

	if len(unique) == 0 {
		return nil
	}

	creates := make([]*ent.MembershipRoleCreate, 0, len(unique))
	for _, roleID := range unique {
		creates = append(creates, tx.MembershipRole.Create().
			SetMembershipID(membershipID).
			SetRoleID(roleID))
	}

	return tx.MembershipRole.CreateBulk(creates...).Exec(ctx)
}

func (r *Orgs) DeclineInvitation(ctx context.Context, invitationID uuid.UUID) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		inv, err := tx.Invitation.Query().
			Where(invitation.ID(invitationID), invitation.AcceptedAtIsNil()).
			Only(ctx)
		if err != nil {
			return err
		}

		if err := recordEnt(ctx, tx, &models.AuthzEvent{
			OrganizationID: &inv.OrganizationID,
			Action:         models.ActionMemberInvitationDeclined,
			Detail:         inv.Email,
		}); err != nil {
			return err
		}

		return tx.Invitation.DeleteOneID(inv.ID).Exec(ctx)
	})

	return translateOrgError("decline invitation", err)
}

// invitation reads one back with its organization and role keys attached.
func (r *Orgs) invitation(ctx context.Context, invitationID uuid.UUID) (*orgs.Invitation, error) {
	row, err := r.db.Ent().Invitation.Get(ctx, invitationID)
	if err != nil {
		return nil, translateOrgError("invitation", err)
	}

	org, err := r.db.Ent().Organization.Get(ctx, row.OrganizationID)
	if err != nil {
		return nil, translateOrgError("invitation organization", err)
	}

	keys, err := r.db.Ent().Role.Query().
		Where(role.HasInvitationRolesWith(invitationrole.InvitationID(invitationID))).
		Order(ent.Asc(role.FieldKey)).
		Select(role.FieldKey).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: invitation roles: %w", err)
	}

	if keys == nil {
		keys = make([]string, 0)
	}

	return &orgs.Invitation{
		ID:           row.ID,
		Organization: organizationModel(org),
		Email:        row.Email,
		InvitedBy:    row.InvitedBy,
		ExpiresAt:    row.ExpiresAt,
		RoleKeys:     keys,
	}, nil
}

func (r *Orgs) InvitationsForOrganization(
	ctx context.Context,
	orgID uuid.UUID,
	now time.Time,
) ([]orgs.Invitation, error) {
	rows, err := r.db.Ent().Invitation.Query().
		Where(
			invitation.OrganizationID(orgID),
			invitation.AcceptedAtIsNil(),
			invitation.ExpiresAtGT(now),
		).
		Order(ent.Desc(invitation.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: invitations for organization: %w", err)
	}

	out := make([]orgs.Invitation, 0, len(rows))
	for _, row := range rows {
		view, err := r.invitation(ctx, row.ID)
		if err != nil {
			return nil, err
		}

		out = append(out, *view)
	}

	return out, nil
}

func (r *Orgs) WithdrawInvitation(ctx context.Context, orgID, invitationID uuid.UUID) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// Scoped by organization: an invitation id from another tenant answers
		// nothing rather than answering truthfully.
		inv, err := tx.Invitation.Query().
			Where(
				invitation.ID(invitationID),
				invitation.OrganizationID(orgID),
				invitation.AcceptedAtIsNil(),
			).
			Only(ctx)
		if err != nil {
			return err
		}

		if err := recordEnt(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionMemberInvitationWithdrawn,
			Detail:         inv.Email,
		}); err != nil {
			return err
		}

		return tx.Invitation.DeleteOneID(inv.ID).Exec(ctx)
	})

	return translateOrgError("withdraw invitation", err)
}

func (r *Orgs) ReissueInvitation(
	ctx context.Context,
	orgID, invitationID uuid.UUID,
	tokenHash string,
	expiresAt time.Time,
) (*orgs.Invitation, error) {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		inv, err := tx.Invitation.Query().
			Where(
				invitation.ID(invitationID),
				invitation.OrganizationID(orgID),
				invitation.AcceptedAtIsNil(),
			).
			ForUpdate().
			Only(ctx)
		if err != nil {
			return err
		}

		if err := tx.Invitation.UpdateOneID(inv.ID).
			SetTokenHash(tokenHash).
			SetExpiresAt(expiresAt.UTC()).
			Exec(ctx); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionMemberInvited,
			Detail:         inv.Email,
		})
	})
	if err != nil {
		return nil, translateOrgError("reissue invitation", err)
	}

	return r.invitation(ctx, invitationID)
}

func organizationModel(row *ent.Organization) models.Organization {
	out := models.Organization{
		Slug: row.Slug,
		Name: row.Name,
	}

	out.ID = row.ID
	out.CreatedAt = row.CreatedAt
	out.UpdatedAt = row.UpdatedAt
	out.IsProtected = row.IsProtected
	if row.DeletedAt != nil {
		out.DeletedAt.Time = *row.DeletedAt
		out.DeletedAt.Valid = true
	}

	return out
}
