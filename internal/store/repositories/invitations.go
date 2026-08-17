package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
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
	invitation := &models.Invitation{
		OrganizationID: orgID,
		Email:          email,
		TokenHash:      tokenHash,
		ExpiresAt:      expiresAt.UTC(),
	}

	if !at.IsZero() {
		invitation.CreatedAt = at.UTC()
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		invitation.InvitedBy = &by
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An address that is already a member gets the same answer as one that
		// already has an invitation. The caller can act on either the same way, and
		// the two do not need telling apart badly enough to justify a second code.
		var member int64
		if err := tx.Table("memberships AS m").
			Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
			Where("m.organization_id = ? AND u.email = ?", orgID, email).
			Count(&member).Error; err != nil {
			return err
		}

		if member > 0 {
			return orgs.ErrAlreadyMember
		}

		if err := tx.Create(invitation).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return orgs.ErrAlreadyMember
			}

			return err
		}

		if err := assignInvitationRoles(tx, orgID, invitation.ID, roleIDs); err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionMemberInvited,
			Detail:         email,
		})
	})
	if err != nil {
		return nil, translateOrgError("invite member", err)
	}

	return r.invitation(ctx, invitation.ID)
}

// assignInvitationRoles is assignRoles for an invitation: the same filtered count
// that refuses a role id belonging to another organization, because an invitation
// is scoped exactly like the membership it will become.
func assignInvitationRoles(tx *gorm.DB, orgID, invitationID uuid.UUID, roleIDs []uuid.UUID) error {
	unique := uniqueIDs(roleIDs)
	if len(unique) == 0 {
		return nil
	}

	var owned int64
	if err := tx.Model(&models.Role{}).
		Where("organization_id = ? AND id IN ?", orgID, unique).
		Count(&owned).Error; err != nil {
		return err
	}

	if int(owned) != len(unique) {
		return orgs.ErrNotFound
	}

	rows := make([]models.InvitationRole, 0, len(unique))
	for _, roleID := range unique {
		rows = append(rows, models.InvitationRole{InvitationID: invitationID, RoleID: roleID})
	}

	return tx.Create(&rows).Error
}

func (r *Orgs) InvitationByToken(ctx context.Context, tokenHash string, now time.Time) (*orgs.Invitation, error) {
	var invitation models.Invitation

	query := r.db.WithContext(ctx).
		Joins("JOIN organizations o ON o.id = invitations.organization_id AND o.deleted_at IS NULL").
		Where("invitations.token_hash = ? AND invitations.accepted_at IS NULL", tokenHash)

	// A zero time means "ignore the clock", which is how the service tells an
	// expired invitation apart from one that never existed.
	if !now.IsZero() {
		query = query.Where("invitations.expires_at > ?", now)
	}

	if err := query.First(&invitation).Error; err != nil {
		return nil, translateOrgError("invitation by token", err)
	}

	return r.invitation(ctx, invitation.ID)
}

func (r *Orgs) InvitationsForEmail(ctx context.Context, email string, now time.Time) ([]orgs.Invitation, error) {
	var rows []models.Invitation

	err := r.db.WithContext(ctx).
		Joins("JOIN organizations o ON o.id = invitations.organization_id AND o.deleted_at IS NULL").
		Where("invitations.email = ? AND invitations.accepted_at IS NULL AND invitations.expires_at > ?",
			email, now).
		Order("invitations.created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: invitations for email: %w", err)
	}

	out := make([]orgs.Invitation, 0, len(rows))

	for i := range rows {
		view, err := r.invitation(ctx, rows[i].ID)
		if err != nil {
			return nil, err
		}

		out = append(out, *view)
	}

	return out, nil
}

func (r *Orgs) AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID, at time.Time) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Locked, so two clients racing the same link cannot both create a
		// membership from it.
		var invitation models.Invitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&invitation, "id = ? AND accepted_at IS NULL", invitationID).Error; err != nil {
			return err
		}

		var roleIDs []uuid.UUID
		if err := tx.Model(&models.InvitationRole{}).
			Where("invitation_id = ?", invitationID).
			Pluck("role_id", &roleIDs).Error; err != nil {
			return err
		}

		uid := userID
		membership := &models.Membership{
			OrganizationID: invitation.OrganizationID,
			UserID:         &uid,
			Email:          invitation.Email,
			Status:         models.MembershipActive,
			InvitedBy:      invitation.InvitedBy,
		}
		membership.Activate(at)

		if err := tx.Create(membership).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return orgs.ErrAlreadyMember
			}

			return err
		}

		if err := assignRoles(tx, invitation.OrganizationID, membership.ID, roleIDs); err != nil {
			return err
		}

		if err := tx.Model(&invitation).Update("accepted_at", at.UTC()).Error; err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &invitation.OrganizationID,
			SubjectID:      &uid,
			Action:         models.ActionMemberAccepted,
		})
	})

	return translateOrgError("accept invitation", err)
}

func (r *Orgs) DeclineInvitation(ctx context.Context, invitationID uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var invitation models.Invitation
		if err := tx.First(&invitation, "id = ? AND accepted_at IS NULL", invitationID).Error; err != nil {
			return err
		}

		if err := record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &invitation.OrganizationID,
			Action:         models.ActionMemberInvitationDeclined,
			Detail:         invitation.Email,
		}); err != nil {
			return err
		}

		return tx.Delete(&invitation).Error
	})

	return translateOrgError("decline invitation", err)
}

// invitation reads one back with its organization and role keys attached.
func (r *Orgs) invitation(ctx context.Context, invitationID uuid.UUID) (*orgs.Invitation, error) {
	var row models.Invitation
	if err := r.db.WithContext(ctx).First(&row, "id = ?", invitationID).Error; err != nil {
		return nil, translateOrgError("invitation", err)
	}

	var org models.Organization
	if err := r.db.WithContext(ctx).First(&org, "id = ?", row.OrganizationID).Error; err != nil {
		return nil, translateOrgError("invitation organization", err)
	}

	var keys []string

	err := r.db.WithContext(ctx).
		Table("invitation_roles AS ir").
		Joins("JOIN roles r ON r.id = ir.role_id").
		Where("ir.invitation_id = ?", invitationID).
		Order("r.key ASC").
		Pluck("r.key", &keys).Error
	if err != nil {
		return nil, fmt.Errorf("store: invitation roles: %w", err)
	}

	return &orgs.Invitation{
		ID:           row.ID,
		Organization: org,
		Email:        row.Email,
		InvitedBy:    row.InvitedBy,
		ExpiresAt:    row.ExpiresAt,
		RoleKeys:     keys,
	}, nil
}
