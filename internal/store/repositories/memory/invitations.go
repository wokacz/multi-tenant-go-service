package memory

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The invitation semantics are copied from the SQL rather than approximated, the
// same way the rest of this fake is: a deleted organization hides its invitations,
// an accepted one is gone from every lookup, the clock is applied in the lookup so
// an expired one is simply not found, and the roles come from the invitation.

func (m *Authz) InviteMember(
	ctx context.Context,
	orgID uuid.UUID,
	email, tokenHash string,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	expiresAt, at time.Time,
) (*orgs.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Already a member, or already invited: one answer, as in Postgres.
	for _, membership := range m.memberships {
		if membership.OrganizationID != orgID || m.accountDeletedLocked(membership) {
			continue
		}

		if m.emailOfLocked(membership.UserID) == email {
			return nil, orgs.ErrAlreadyMember
		}
	}

	for _, existing := range m.invitations {
		if existing.OrganizationID == orgID && existing.Email == email && existing.AcceptedAt == nil {
			return nil, orgs.ErrAlreadyMember
		}
	}

	if err := m.rolesBelongLocked(orgID, roleIDs); err != nil {
		return nil, err
	}

	invitation := &models.Invitation{
		OrganizationID: orgID,
		Email:          email,
		TokenHash:      tokenHash,
		ExpiresAt:      expiresAt.UTC(),
	}
	invitation.ID = uuid.Must(uuid.NewV7())

	if !at.IsZero() {
		invitation.CreatedAt = at.UTC()
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		invitation.InvitedBy = &by
	}

	m.invitations[invitation.ID] = invitation
	m.inviteRoles[invitation.ID] = uniqueIDs(roleIDs)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		Action:         models.ActionMemberInvited,
		Detail:         email,
	})

	view := m.invitationLocked(invitation)

	return &view, nil
}

func (m *Authz) InvitationByToken(_ context.Context, tokenHash string, now time.Time) (*orgs.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, invitation := range m.invitations {
		if invitation.TokenHash != tokenHash || invitation.AcceptedAt != nil {
			continue
		}

		if org, ok := m.orgs[invitation.OrganizationID]; !ok || org.IsDeleted() {
			continue
		}

		// A zero time ignores the clock, which is how the service tells an expired
		// invitation apart from one that never existed.
		if !now.IsZero() && !invitation.ExpiresAt.After(now) {
			continue
		}

		view := m.invitationLocked(invitation)

		return &view, nil
	}

	return nil, orgs.ErrNotFound
}

func (m *Authz) InvitationsForEmail(_ context.Context, email string, now time.Time) ([]orgs.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := []orgs.Invitation{}

	for _, invitation := range m.invitations {
		if invitation.Email != email || invitation.AcceptedAt != nil {
			continue
		}

		if org, ok := m.orgs[invitation.OrganizationID]; !ok || org.IsDeleted() {
			continue
		}

		if !invitation.ExpiresAt.After(now) {
			continue
		}

		out = append(out, m.invitationLocked(invitation))
	}

	slices.SortFunc(out, func(a, b orgs.Invitation) int {
		return b.ExpiresAt.Compare(a.ExpiresAt)
	})

	return out, nil
}

func (m *Authz) AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	invitation, ok := m.invitations[invitationID]
	if !ok || invitation.AcceptedAt != nil {
		return orgs.ErrNotFound
	}

	for _, existing := range m.memberships {
		if existing.OrganizationID == invitation.OrganizationID && sameAccount(existing, userID) {
			return orgs.ErrAlreadyMember
		}
	}

	membership := &models.Membership{
		OrganizationID: invitation.OrganizationID,
		UserID:         userID,
		Status:         models.MembershipActive,
		InvitedBy:      invitation.InvitedBy,
	}
	membership.ID = uuid.Must(uuid.NewV7())
	membership.Activate(at)

	m.memberships[membership.ID] = membership
	m.memberRoles[membership.ID] = slices.Clone(m.inviteRoles[invitationID])

	accepted := at.UTC()
	invitation.AcceptedAt = &accepted

	subject := userID
	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &invitation.OrganizationID,
		SubjectID:      &subject,
		Action:         models.ActionMemberAccepted,
	})

	return nil
}

func (m *Authz) DeclineInvitation(ctx context.Context, invitationID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	invitation, ok := m.invitations[invitationID]
	if !ok || invitation.AcceptedAt != nil {
		return orgs.ErrNotFound
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &invitation.OrganizationID,
		Action:         models.ActionMemberInvitationDeclined,
		Detail:         invitation.Email,
	})

	delete(m.invitations, invitationID)
	delete(m.inviteRoles, invitationID)

	return nil
}

func (m *Authz) invitationLocked(invitation *models.Invitation) orgs.Invitation {
	keys := []string{}

	for _, roleID := range m.inviteRoles[invitation.ID] {
		if role, ok := m.roles[roleID]; ok {
			keys = append(keys, role.Key)
		}
	}

	slices.Sort(keys)

	view := orgs.Invitation{
		ID:        invitation.ID,
		Email:     invitation.Email,
		InvitedBy: invitation.InvitedBy,
		ExpiresAt: invitation.ExpiresAt,
		RoleKeys:  keys,
	}

	if org, ok := m.orgs[invitation.OrganizationID]; ok {
		view.Organization = *org
	}

	return view
}

// SeedInvitation stores an invitation whose token is the given string, so a test
// can hold the secret the way an invitee would.
func (m *Authz) SeedInvitation(orgID uuid.UUID, email, token string, expiresAt time.Time, roleIDs ...uuid.UUID) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()

	invitation := &models.Invitation{
		OrganizationID: orgID,
		Email:          email,
		TokenHash:      orgs.HashInvitationToken(token),
		ExpiresAt:      expiresAt.UTC(),
	}
	invitation.ID = uuid.Must(uuid.NewV7())

	m.invitations[invitation.ID] = invitation
	m.inviteRoles[invitation.ID] = uniqueIDs(roleIDs)

	return invitation.ID
}

func (m *Authz) InvitationsForOrganization(
	_ context.Context,
	orgID uuid.UUID,
	now time.Time,
) ([]orgs.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := []orgs.Invitation{}

	for _, invitation := range m.invitations {
		if invitation.OrganizationID != orgID || invitation.AcceptedAt != nil {
			continue
		}

		if !invitation.ExpiresAt.After(now) {
			continue
		}

		out = append(out, m.invitationLocked(invitation))
	}

	slices.SortFunc(out, func(a, b orgs.Invitation) int {
		return b.ExpiresAt.Compare(a.ExpiresAt)
	})

	return out, nil
}

func (m *Authz) WithdrawInvitation(ctx context.Context, orgID, invitationID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	invitation, ok := m.invitations[invitationID]
	if !ok || invitation.OrganizationID != orgID || invitation.AcceptedAt != nil {
		return orgs.ErrNotFound
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		Action:         models.ActionMemberInvitationWithdrawn,
		Detail:         invitation.Email,
	})

	delete(m.invitations, invitationID)
	delete(m.inviteRoles, invitationID)

	return nil
}

func (m *Authz) ReissueInvitation(
	ctx context.Context,
	orgID, invitationID uuid.UUID,
	tokenHash string,
	expiresAt time.Time,
) (*orgs.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	invitation, ok := m.invitations[invitationID]
	if !ok || invitation.OrganizationID != orgID || invitation.AcceptedAt != nil {
		return nil, orgs.ErrNotFound
	}

	invitation.TokenHash = tokenHash
	invitation.ExpiresAt = expiresAt.UTC()

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		Action:         models.ActionMemberInvited,
		Detail:         invitation.Email,
	})

	view := m.invitationLocked(invitation)

	return &view, nil
}
