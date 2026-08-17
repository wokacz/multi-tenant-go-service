package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// Authz is an in-memory implementation of the whole authorization surface.
//
// Like Users, it exists so the API tests and the domain tests exercise one fake
// rather than a stub each. The semantics that decide access are copied
// deliberately rather than approximated: a suspended or invited membership
// resolves to ErrNotMember exactly as the SQL does, a member holding no roles
// resolves to an empty grant rather than to ErrNotMember, unknown permission
// keys are handed back raw so that the catalog — not the store — is what drops
// them, and a role id from another organization is not found rather than
// silently usable.
//
// One type satisfies all three interfaces because the three describe one
// subject: who belongs to which organization and what that lets them do.
// Splitting it would give the tests two views of that state and let them
// disagree.
//
// Methods prefixed Seed are fixtures. They are here rather than in each test
// package because building an organization by hand is the setup every
// authorization test needs, and three copies of it would disagree about what "a
// member" means.
type Authz struct {
	mu sync.Mutex

	// users supplies the names and addresses the member queries join to. nil is
	// allowed for tests that never list members.
	users *Users

	orgs         map[uuid.UUID]*models.Organization
	memberships  map[uuid.UUID]*models.Membership
	roles        map[uuid.UUID]*models.Role
	rolePerms    map[uuid.UUID][]string
	memberRoles  map[uuid.UUID][]uuid.UUID
	systemRoles  map[uuid.UUID][]string
	deletedUsers map[uuid.UUID]bool

	// events is append-only, the way the table is. Writes go through
	// recordLocked so the "no actor, no row" rule is copied exactly rather than
	// approximated — a test that passed here and failed against Postgres would
	// be worse than no test.
	events []models.AuthzEvent
}

// Compile-time checks, the same ones the GORM implementations carry.
var (
	_ authz.Repository = (*Authz)(nil)
	_ orgs.Repository  = (*Authz)(nil)
	_ orgs.Directory   = (*Authz)(nil)
	_ orgs.Provisioner = (*Authz)(nil)

	_ audit.Reader         = (*Authz)(nil)
	_ audit.PlatformReader = (*Authz)(nil)
)

func NewAuthz(users *Users) *Authz {
	return &Authz{
		users:        users,
		orgs:         map[uuid.UUID]*models.Organization{},
		memberships:  map[uuid.UUID]*models.Membership{},
		roles:        map[uuid.UUID]*models.Role{},
		rolePerms:    map[uuid.UUID][]string{},
		memberRoles:  map[uuid.UUID][]uuid.UUID{},
		systemRoles:  map[uuid.UUID][]string{},
		deletedUsers: map[uuid.UUID]bool{},
	}
}

// recordLocked mirrors the store's record: nothing is written without an actor.
func (m *Authz) recordLocked(ctx context.Context, event models.AuthzEvent) {
	actor := audit.ActorFrom(ctx)
	if actor.IsZero() {
		return
	}

	event.ID = uuid.Must(uuid.NewV7())
	event.CreatedAt = time.Now().UTC()
	event.ActorID = actor.ID
	event.IP = actor.IP
	event.UserAgent = actor.UserAgent

	if event.IP == "" {
		event.IP = "0.0.0.0"
	}

	m.events = append(m.events, event)
}

// subjectOfLocked resolves a membership to the account it belongs to, so an
// entry survives the membership being deleted.
func (m *Authz) subjectOfLocked(memberID uuid.UUID) *uuid.UUID {
	if membership, ok := m.memberships[memberID]; ok {
		return membership.UserID
	}

	return nil
}

func memberSortKey(m orgs.Member) string {
	if m.Name != "" {
		return m.Name
	}

	return m.Email
}

func sameAccount(membership *models.Membership, userID uuid.UUID) bool {
	return membership != nil && membership.UserID != nil && *membership.UserID == userID
}

func (m *Authz) accountDeletedLocked(membership *models.Membership) bool {
	return membership.UserID != nil && m.deletedUsers[*membership.UserID]
}

func (m *Authz) emailOfLocked(userID uuid.UUID) string {
	if m.users != nil {
		if u, err := m.users.ByID(context.Background(), userID); err == nil {
			return u.Email
		}
	}

	return userID.String() + "@seed.test"
}

func ptrID(id uuid.UUID) *uuid.UUID {
	copy := id

	return &copy
}

func (m *Authz) Events(_ context.Context, orgID uuid.UUID, limit, offset int) ([]audit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.readEventsLocked(func(e *models.AuthzEvent) bool {
		return e.OrganizationID != nil && *e.OrganizationID == orgID
	}, limit, offset), nil
}

func (m *Authz) AllEvents(_ context.Context, limit, offset int) ([]audit.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.readEventsLocked(func(*models.AuthzEvent) bool { return true }, limit, offset), nil
}

func (m *Authz) readEventsLocked(keep func(*models.AuthzEvent) bool, limit, offset int) []audit.Event {
	out := make([]audit.Event, 0, len(m.events))

	// Newest first, like the SQL's ORDER BY created_at DESC.
	for i := len(m.events) - 1; i >= 0; i-- {
		row := m.events[i]
		if !keep(&row) {
			continue
		}

		event := audit.Event{
			ID:             row.ID,
			At:             row.CreatedAt,
			OrganizationID: row.OrganizationID,
			Action:         row.Action,
			Actor:          audit.Party{ID: row.ActorID},
			RoleID:         row.RoleID,
			Detail:         row.Detail,
			IP:             row.IP,
			UserAgent:      row.UserAgent,
		}

		if m.users != nil {
			if u, err := m.users.ByID(context.Background(), row.ActorID); err == nil {
				event.Actor.Name, event.Actor.Email = u.Name, u.Email
			}
		}

		if row.SubjectID != nil {
			event.Subject = &audit.Party{ID: *row.SubjectID}
		}

		if row.RoleID != nil {
			if role, ok := m.roles[*row.RoleID]; ok {
				event.RoleKey = role.Key
			}
		}

		out = append(out, event)
	}

	if offset >= len(out) {
		return nil
	}

	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}

	return out
}

// --- authz.Repository ---

func (m *Authz) OrganizationPermissionKeys(_ context.Context, userID, orgID uuid.UUID) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership := m.activeMembershipLocked(userID, orgID)
	if membership == nil {
		return nil, authz.ErrNotMember
	}

	var keys []string

	for _, roleID := range m.memberRoles[membership.ID] {
		if _, ok := m.roles[roleID]; !ok {
			// A role deleted out from under the assignment grants nothing. The
			// SQL gets this from its foreign key cascade.
			continue
		}

		for _, key := range m.rolePerms[roleID] {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}

	return keys, nil
}

func (m *Authz) PermissionKeysByOrganization(_ context.Context, userID uuid.UUID) (map[uuid.UUID][]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := map[uuid.UUID][]string{}

	for _, membership := range m.memberships {
		if !sameAccount(membership, userID) {
			continue
		}

		if m.activeMembershipLocked(userID, membership.OrganizationID) == nil {
			continue
		}

		var keys []string

		for _, roleID := range m.memberRoles[membership.ID] {
			if _, ok := m.roles[roleID]; !ok {
				continue
			}

			for _, key := range m.rolePerms[roleID] {
				if !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
		}

		// An organization the caller holds nothing in is left out, matching the
		// inner join the SQL uses.
		if len(keys) > 0 {
			out[membership.OrganizationID] = keys
		}
	}

	return out, nil
}

func (m *Authz) SystemRoleKeys(_ context.Context, userID uuid.UUID) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deletedUsers[userID] {
		return nil, nil
	}

	return slices.Clone(m.systemRoles[userID]), nil
}

// --- orgs.Directory ---

func (m *Authz) MembershipsForUser(_ context.Context, userID uuid.UUID) ([]orgs.Membership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []orgs.Membership

	for _, membership := range m.memberships {
		matchesAccount := sameAccount(membership, userID)
		matchesInvite := membership.Status == models.MembershipInvited &&
			membership.UserID == nil &&
			membership.Email == m.emailOfLocked(userID)
		if !matchesAccount && !matchesInvite {
			continue
		}

		org, ok := m.orgs[membership.OrganizationID]
		if !ok || org.IsDeleted() {
			continue
		}

		var keys []string

		for _, roleID := range m.memberRoles[membership.ID] {
			if role, ok := m.roles[roleID]; ok {
				keys = append(keys, role.Key)
			}
		}

		slices.Sort(keys)

		out = append(out, orgs.Membership{
			ID:           membership.ID,
			Organization: *org,
			Status:       membership.Status,
			RoleKeys:     keys,
		})
	}

	// The SQL orders by name; map iteration does not, and a test asserting on
	// the first element would otherwise pass or fail at random.
	slices.SortFunc(out, func(a, b orgs.Membership) int {
		return strings.Compare(a.Organization.Name, b.Organization.Name)
	})

	return out, nil
}

// --- orgs.Repository ---

func (m *Authz) Organization(_ context.Context, orgID uuid.UUID) (*models.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.organizationLocked(orgID)
}

func (m *Authz) UpdateOrganization(ctx context.Context, orgID uuid.UUID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	org, err := m.organizationLocked(orgID)
	if err != nil {
		return err
	}

	m.orgs[orgID].Name = name
	_ = org

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionOrganizationUpdated, Detail: name,
	})

	return nil
}

func (m *Authz) DeleteOrganization(ctx context.Context, orgID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.organizationLocked(orgID); err != nil {
		return err
	}

	// Delete() carries the protection rule, so the default organization is
	// refused here exactly as it is in Postgres.
	if err := m.orgs[orgID].Delete(); err != nil {
		return err
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionOrganizationDeleted,
	})

	return nil
}

func (m *Authz) Members(_ context.Context, orgID uuid.UUID) ([]orgs.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []orgs.Member

	for _, membership := range m.memberships {
		if membership.OrganizationID != orgID || m.accountDeletedLocked(membership) {
			continue
		}

		out = append(out, m.memberLocked(membership))
	}

	slices.SortFunc(out, func(a, b orgs.Member) int {
		return strings.Compare(memberSortKey(a), memberSortKey(b))
	})

	return out, nil
}

func (m *Authz) Member(_ context.Context, orgID, memberID uuid.UUID) (*orgs.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	// Scoped by organization, so a membership id from another tenant simply
	// does not match.
	if !ok || membership.OrganizationID != orgID || m.accountDeletedLocked(membership) {
		return nil, orgs.ErrNotFound
	}

	member := m.memberLocked(membership)

	return &member, nil
}

func (m *Authz) AddMember(
	ctx context.Context,
	orgID, userID uuid.UUID,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	at time.Time,
) (*orgs.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	email := m.emailOfLocked(userID)

	for _, membership := range m.memberships {
		if membership.OrganizationID != orgID {
			continue
		}

		if sameAccount(membership, userID) {
			return nil, orgs.ErrAlreadyMember
		}

		if membership.Email != email {
			continue
		}

		if membership.Status != models.MembershipInvited || membership.UserID != nil {
			return nil, orgs.ErrAlreadyMember
		}

		if err := m.rolesBelongLocked(orgID, roleIDs); err != nil {
			return nil, err
		}

		membership.UserID = ptrID(userID)
		membership.Activate(at)
		m.memberRoles[membership.ID] = uniqueIDs(roleIDs)

		m.recordLocked(ctx, models.AuthzEvent{
			OrganizationID: &orgID, SubjectID: &userID, Action: models.ActionMemberAccepted,
		})

		member := m.memberLocked(membership)

		return &member, nil
	}

	if err := m.rolesBelongLocked(orgID, roleIDs); err != nil {
		return nil, err
	}

	id := uuid.Must(uuid.NewV7())
	membership := &models.Membership{
		Model:          models.Model{ID: id},
		UserID:         ptrID(userID),
		Email:          m.emailOfLocked(userID),
		OrganizationID: orgID,
		Status:         models.MembershipActive,
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		membership.InvitedBy = &by
	}

	membership.Activate(at)

	m.memberships[id] = membership
	m.memberRoles[id] = uniqueIDs(roleIDs)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, SubjectID: &userID, Action: models.ActionMemberJoined,
	})

	member := m.memberLocked(membership)

	return &member, nil
}

func (m *Authz) InviteMember(
	ctx context.Context,
	orgID uuid.UUID,
	email string,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	at time.Time,
) (*orgs.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, membership := range m.memberships {
		if membership.OrganizationID == orgID && membership.Email == email {
			return nil, orgs.ErrAlreadyMember
		}
	}

	if err := m.rolesBelongLocked(orgID, roleIDs); err != nil {
		return nil, err
	}

	id := uuid.Must(uuid.NewV7())
	membership := &models.Membership{
		Model:          models.Model{ID: id, CreatedAt: at.UTC()},
		Email:          email,
		OrganizationID: orgID,
		Status:         models.MembershipInvited,
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		membership.InvitedBy = &by
	}

	m.memberships[id] = membership
	m.memberRoles[id] = uniqueIDs(roleIDs)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionMemberInvited, Detail: email,
	})

	member := m.memberLocked(membership)

	return &member, nil
}

func (m *Authz) SetMemberStatus(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	status models.MembershipStatus,
	at time.Time,
	guard orgs.OwnerGuard,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	if !ok || membership.OrganizationID != orgID {
		return orgs.ErrNotFound
	}

	if err := m.applyOwnerGuardLocked(orgID, memberID, guard); err != nil {
		return err
	}

	action := models.ActionMemberSuspended
	if status.GrantsPermissions() {
		membership.Activate(at)
		action = models.ActionMemberReinstated
	} else {
		membership.Status = status
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		SubjectID:      m.subjectOfLocked(memberID),
		Action:         action,
		Detail:         string(status),
	})

	return nil
}

func (m *Authz) RemoveMember(ctx context.Context, orgID, memberID uuid.UUID, guard orgs.OwnerGuard) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	if !ok || membership.OrganizationID != orgID {
		return orgs.ErrNotFound
	}

	if err := m.applyOwnerGuardLocked(orgID, memberID, guard); err != nil {
		return err
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		SubjectID:      m.subjectOfLocked(memberID),
		Action:         models.ActionMemberRemoved,
	})

	delete(m.memberships, memberID)
	delete(m.memberRoles, memberID)

	return nil
}

func (m *Authz) ReplaceMemberRoles(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	roleIDs []uuid.UUID,
	guard orgs.OwnerGuard,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	if !ok || membership.OrganizationID != orgID {
		return orgs.ErrNotFound
	}

	if err := m.applyOwnerGuardLocked(orgID, memberID, guard); err != nil {
		return err
	}

	if err := m.rolesBelongLocked(orgID, roleIDs); err != nil {
		return err
	}

	m.memberRoles[memberID] = uniqueIDs(roleIDs)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID,
		SubjectID:      m.subjectOfLocked(memberID),
		Action:         models.ActionMemberRolesChanged,
	})

	return nil
}

func (m *Authz) Roles(_ context.Context, orgID uuid.UUID) ([]orgs.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []orgs.Role

	for _, role := range m.roles {
		if role.OrganizationID == orgID {
			out = append(out, m.roleLocked(role))
		}
	}

	slices.SortFunc(out, func(a, b orgs.Role) int {
		return strings.Compare(a.Key, b.Key)
	})

	return out, nil
}

func (m *Authz) Role(_ context.Context, orgID, roleID uuid.UUID) (*orgs.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	role, ok := m.roles[roleID]
	if !ok || role.OrganizationID != orgID {
		return nil, orgs.ErrNotFound
	}

	decorated := m.roleLocked(role)

	return &decorated, nil
}

func (m *Authz) CreateRole(
	ctx context.Context,
	orgID uuid.UUID,
	role *models.Role,
	permissions []authz.Permission,
) (*orgs.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.roles {
		if existing.OrganizationID == orgID && existing.Key == role.Key {
			return nil, orgs.ErrRoleKeyTaken
		}
	}

	if role.ID == uuid.Nil {
		role.ID = uuid.Must(uuid.NewV7())
	}

	role.OrganizationID = orgID

	stored := *role
	m.roles[role.ID] = &stored
	m.rolePerms[role.ID] = permissionStrings(permissions)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionRoleCreated,
		RoleID: &stored.ID, Detail: stored.Key,
	})

	decorated := m.roleLocked(&stored)

	return &decorated, nil
}

func (m *Authz) UpdateRole(ctx context.Context, orgID, roleID uuid.UUID, name, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	role, ok := m.roles[roleID]
	if !ok || role.OrganizationID != orgID || role.IsSystem {
		return orgs.ErrNotFound
	}

	role.Name = name
	role.Description = description

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionRoleUpdated,
		RoleID: &roleID, Detail: name,
	})

	return nil
}

func (m *Authz) DeleteRole(ctx context.Context, orgID, roleID uuid.UUID, guard orgs.RoleGuard) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	role, ok := m.roles[roleID]
	if !ok || role.OrganizationID != orgID {
		return orgs.ErrNotFound
	}

	holders := 0

	for _, assigned := range m.memberRoles {
		if slices.Contains(assigned, roleID) {
			holders++
		}
	}

	if err := guard(holders); err != nil {
		return err
	}

	// BeforeDelete carries the protection rule, so a system role is refused
	// here exactly as it is in Postgres.
	if err := role.BeforeDelete(nil); err != nil {
		return err
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionRoleDeleted,
		RoleID: &roleID, Detail: role.Key,
	})

	delete(m.roles, roleID)
	delete(m.rolePerms, roleID)

	for membershipID, assigned := range m.memberRoles {
		m.memberRoles[membershipID] = slices.DeleteFunc(slices.Clone(assigned), func(id uuid.UUID) bool {
			return id == roleID
		})
	}

	return nil
}

func (m *Authz) ReplaceRolePermissions(
	ctx context.Context,
	orgID, roleID uuid.UUID,
	permissions []authz.Permission,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	role, ok := m.roles[roleID]
	if !ok || role.OrganizationID != orgID {
		return orgs.ErrNotFound
	}

	m.rolePerms[roleID] = permissionStrings(permissions)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &orgID, Action: models.ActionRolePermissionsChanged,
		RoleID: &roleID,
	})

	return nil
}

// --- orgs.Provisioner ---

func (m *Authz) OrganizationBySlug(_ context.Context, slug string) (*models.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, org := range m.orgs {
		if org.Slug == slug && !org.IsDeleted() {
			stored := *org

			return &stored, nil
		}
	}

	return nil, orgs.ErrNotFound
}

func (m *Authz) CreateOrganization(
	ctx context.Context,
	org *models.Organization,
	roles []authz.RoleDefinition,
) (*models.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.orgs {
		if existing.Slug == org.Slug && !existing.IsDeleted() {
			return nil, orgs.ErrSlugTaken
		}
	}

	if org.ID == uuid.Nil {
		org.ID = uuid.Must(uuid.NewV7())
	}

	stored := *org
	m.orgs[org.ID] = &stored

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &stored.ID, Action: models.ActionOrganizationCreated, Detail: stored.Slug,
	})

	for _, def := range roles {
		id := uuid.Must(uuid.NewV7())
		m.roles[id] = &models.Role{
			Model:          models.Model{ID: id},
			OrganizationID: org.ID,
			Key:            string(def.Key),
			Name:           def.Name,
			Description:    def.Description,
			IsSystem:       true,
		}
		m.rolePerms[id] = permissionStrings(def.Permissions)
	}

	return &stored, nil
}

func (m *Authz) AllOrganizations(_ context.Context, limit, offset int) ([]models.Organization, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]models.Organization, 0, len(m.orgs))
	for _, org := range m.orgs {
		if !org.IsDeleted() {
			out = append(out, *org)
		}
	}

	// The SQL orders by id descending; map iteration does not.
	slices.SortFunc(out, func(a, b models.Organization) int {
		return strings.Compare(b.ID.String(), a.ID.String())
	})

	if offset >= len(out) {
		return nil, nil
	}

	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}

	return out, nil
}

func (m *Authz) GrantSystemRole(
	_ context.Context,
	userID uuid.UUID,
	key authz.RoleKey,
	_ uuid.UUID,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !slices.Contains(m.systemRoles[userID], string(key)) {
		m.systemRoles[userID] = append(m.systemRoles[userID], string(key))
	}

	return nil
}

func (m *Authz) RoleByKey(_ context.Context, orgID uuid.UUID, key string) (*orgs.Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, role := range m.roles {
		if role.OrganizationID == orgID && role.Key == key {
			decorated := m.roleLocked(role)

			return &decorated, nil
		}
	}

	return nil, orgs.ErrNotFound
}

func (m *Authz) MemberPermissions(_ context.Context, orgID, memberID uuid.UUID) ([]authz.Permission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	if !ok || membership.OrganizationID != orgID {
		return nil, nil
	}

	seen := map[authz.Permission]struct{}{}
	out := []authz.Permission{}

	for _, roleID := range m.memberRoles[memberID] {
		// A role that has been deleted takes its permissions with it, the same
		// way the join in Postgres finds nothing for it.
		if _, ok := m.roles[roleID]; !ok {
			continue
		}

		for _, key := range m.rolePerms[roleID] {
			perm := authz.Permission(key)
			if _, dup := seen[perm]; dup {
				continue
			}

			seen[perm] = struct{}{}
			out = append(out, perm)
		}
	}

	slices.SortFunc(out, func(a, b authz.Permission) int {
		return strings.Compare(string(a), string(b))
	})

	return out, nil
}

func (m *Authz) MemberByUser(_ context.Context, orgID, userID uuid.UUID) (*orgs.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, membership := range m.memberships {
		if membership.OrganizationID != orgID || !sameAccount(membership, userID) {
			continue
		}

		if m.accountDeletedLocked(membership) {
			continue
		}

		member := m.memberLocked(membership)

		return &member, nil
	}

	return nil, orgs.ErrNotFound
}

// --- helpers, all called with the lock held ---

func (m *Authz) organizationLocked(orgID uuid.UUID) (*models.Organization, error) {
	org, ok := m.orgs[orgID]
	if !ok || org.IsDeleted() {
		return nil, orgs.ErrNotFound
	}

	stored := *org

	return &stored, nil
}

// activeMembershipLocked applies every condition the SQL applies: the user is
// live, the organization is live, the row exists, and its status grants.
func (m *Authz) activeMembershipLocked(userID, orgID uuid.UUID) *models.Membership {
	if m.deletedUsers[userID] {
		return nil
	}

	org, ok := m.orgs[orgID]
	if !ok || org.IsDeleted() {
		return nil
	}

	for _, membership := range m.memberships {
		if !sameAccount(membership, userID) || membership.OrganizationID != orgID {
			continue
		}

		if !membership.Status.GrantsPermissions() {
			return nil
		}

		return membership
	}

	return nil
}

func (m *Authz) memberLocked(membership *models.Membership) orgs.Member {
	member := orgs.Member{
		ID:       membership.ID,
		UserID:   membership.AccountID(),
		Email:    membership.Email,
		Status:   membership.Status,
		JoinedAt: membership.JoinedAt,
		Roles:    []orgs.RoleSummary{},
	}

	if member.UserID != uuid.Nil && m.users != nil {
		if u, err := m.users.ByID(context.Background(), member.UserID); err == nil {
			member.Name, member.Email = u.Name, u.Email
		}
	}

	for _, roleID := range m.memberRoles[membership.ID] {
		if role, ok := m.roles[roleID]; ok {
			member.Roles = append(member.Roles, orgs.RoleSummary{
				ID: role.ID, Key: role.Key, Name: role.Name, IsSystem: role.IsSystem,
			})
		}
	}

	slices.SortFunc(member.Roles, func(a, b orgs.RoleSummary) int {
		return strings.Compare(a.Key, b.Key)
	})

	return member
}

func (m *Authz) roleLocked(role *models.Role) orgs.Role {
	permissions := make([]authz.Permission, 0, len(m.rolePerms[role.ID]))
	for _, key := range m.rolePerms[role.ID] {
		permissions = append(permissions, authz.Permission(key))
	}

	slices.Sort(permissions)

	members := 0

	for _, assigned := range m.memberRoles {
		if slices.Contains(assigned, role.ID) {
			members++
		}
	}

	return orgs.Role{Role: *role, Permissions: permissions, Members: members}
}

// rolesBelongLocked refuses a role id that is not this organization's, which is
// what stops a role being borrowed across tenants. The SQL gets it from a
// filtered count for the same reason: the foreign key only says the role exists
// somewhere.
func (m *Authz) rolesBelongLocked(orgID uuid.UUID, roleIDs []uuid.UUID) error {
	for _, roleID := range roleIDs {
		role, ok := m.roles[roleID]
		if !ok || role.OrganizationID != orgID {
			return orgs.ErrNotFound
		}
	}

	return nil
}

func permissionStrings(permissions []authz.Permission) []string {
	out := make([]string, 0, len(permissions))
	for _, perm := range permissions {
		out = append(out, string(perm))
	}

	return out
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))

	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}

	return out
}

// --- fixtures ---

// SeedOrganization creates an organization and returns its id.
func (m *Authz) SeedOrganization(slug, name string) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.Must(uuid.NewV7())
	m.orgs[id] = &models.Organization{Model: models.Model{ID: id}, Slug: slug, Name: name}

	return id
}

// SeedProtectedOrganization creates one that refuses deletion, the way the
// default organization is set up.
func (m *Authz) SeedProtectedOrganization(slug, name string) uuid.UUID {
	id := m.SeedOrganization(slug, name)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.orgs[id].IsProtected = true

	return id
}

// SeedSoftDeletedOrganization marks an organization deleted, which must stop it
// granting anything.
func (m *Authz) SeedSoftDeletedOrganization(orgID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if org, ok := m.orgs[orgID]; ok {
		org.IsProtected = false
		_ = org.Delete()
	}
}

// SeedSoftDeletedUser marks an account deleted.
func (m *Authz) SeedSoftDeletedUser(userID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.deletedUsers[userID] = true
}

// SeedRole creates a role with the given permission keys, written as given —
// including keys the catalog does not define, so a test can prove those are
// dropped later.
func (m *Authz) SeedRole(orgID uuid.UUID, key string, permissions ...string) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.Must(uuid.NewV7())
	m.roles[id] = &models.Role{
		Model:          models.Model{ID: id},
		OrganizationID: orgID,
		Key:            key,
		Name:           key,
	}
	m.rolePerms[id] = slices.Clone(permissions)

	return id
}

// SeedShippedRole materialises one of the catalog's roles into the
// organization, the way creating an organization does in production.
func (m *Authz) SeedShippedRole(orgID uuid.UUID, key authz.RoleKey) uuid.UUID {
	def, ok := authz.LookupRole(key)
	if !ok {
		panic("memory: no shipped role named " + string(key))
	}

	id := m.SeedRole(orgID, string(key), permissionStrings(def.Permissions)...)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.roles[id].IsSystem = true
	m.roles[id].Name = def.Name

	return id
}

// SeedDeleteRole removes a role, leaving any assignment of it behind so a test
// can prove the dangling assignment grants nothing.
func (m *Authz) SeedDeleteRole(roleID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.roles, roleID)
	delete(m.rolePerms, roleID)
}

// SeedMember puts a user in an organization with the given status and roles.
func (m *Authz) SeedMember(
	orgID, userID uuid.UUID,
	status models.MembershipStatus,
	roleIDs ...uuid.UUID,
) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.Must(uuid.NewV7())
	uid := userID
	m.memberships[id] = &models.Membership{
		Model:          models.Model{ID: id},
		UserID:         &uid,
		Email:          m.emailOfLocked(userID),
		OrganizationID: orgID,
		Status:         status,
	}
	m.memberRoles[id] = slices.Clone(roleIDs)

	return id
}

// SeedMemberStatus changes a membership's status without going through the
// service, so a test can arrange a state the rules would refuse to create.
func (m *Authz) SeedMemberStatus(membershipID uuid.UUID, status models.MembershipStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if membership, ok := m.memberships[membershipID]; ok {
		membership.Status = status
	}
}

// SeedMemberRoles replaces a membership's role assignments.
func (m *Authz) SeedMemberRoles(membershipID uuid.UUID, roleIDs ...uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.memberRoles[membershipID] = slices.Clone(roleIDs)
}

// SeedSystemRole assigns an installation-wide role by key.
func (m *Authz) SeedSystemRole(userID uuid.UUID, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !slices.Contains(m.systemRoles[userID], key) {
		m.systemRoles[userID] = append(m.systemRoles[userID], key)
	}
}

func (m *Authz) AcceptInvitation(
	ctx context.Context,
	memberID, userID uuid.UUID,
	email string,
	at time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.acceptInvitationLocked(ctx, memberID, userID, email, at)
}

func (m *Authz) DeclineInvitation(ctx context.Context, memberID uuid.UUID, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	membership, ok := m.memberships[memberID]
	if !ok || membership.Status != models.MembershipInvited || membership.Email != email {
		return orgs.ErrNotFound
	}

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &membership.OrganizationID,
		Action:         models.ActionMemberRemoved,
		Detail:         email,
	})

	delete(m.memberships, memberID)
	delete(m.memberRoles, memberID)

	return nil
}

func (m *Authz) acceptInvitationLocked(
	ctx context.Context,
	memberID, userID uuid.UUID,
	email string,
	at time.Time,
) error {
	membership, ok := m.memberships[memberID]
	if !ok || membership.Status != models.MembershipInvited || membership.Email != email {
		return orgs.ErrNotFound
	}

	// A deleted organization keeps its invitations, and accepting into one would
	// produce an active membership that every read then filters out.
	if org, ok := m.orgs[membership.OrganizationID]; !ok || org.IsDeleted() {
		return orgs.ErrNotFound
	}

	for _, existing := range m.memberships {
		if existing.ID != memberID && sameAccount(existing, userID) && existing.OrganizationID == membership.OrganizationID {
			return orgs.ErrAlreadyMember
		}
	}

	membership.UserID = ptrID(userID)
	membership.Activate(at)

	m.recordLocked(ctx, models.AuthzEvent{
		OrganizationID: &membership.OrganizationID,
		SubjectID:      &userID,
		Action:         models.ActionMemberAccepted,
	})

	return nil
}

// applyOwnerGuardLocked hands the domain the same facts Postgres reads inside its
// transaction. The mutex here plays the part the organization row lock plays
// there: nothing else can be counting or writing while the guard decides.
func (m *Authz) applyOwnerGuardLocked(orgID, memberID uuid.UUID, guard orgs.OwnerGuard) error {
	return guard(m.ownerStateLocked(orgID, memberID))
}

// ownerStateLocked counts active owners with live accounts, and says whether the
// subject is one of them. Both answers come from the same walk, so they cannot
// disagree about a membership that outlived its account — the disagreement that
// once made such a row impossible to remove.
func (m *Authz) ownerStateLocked(orgID, memberID uuid.UUID) orgs.OwnerState {
	state := orgs.OwnerState{}

	for _, membership := range m.memberships {
		if membership.OrganizationID != orgID || !membership.Status.GrantsPermissions() {
			continue
		}

		if m.accountDeletedLocked(membership) {
			continue
		}

		if !m.holdsOwnerLocked(membership.ID) {
			continue
		}

		state.Owners++

		if membership.ID == memberID {
			state.SubjectHoldsOwner = true
		}
	}

	return state
}

func (m *Authz) holdsOwnerLocked(memberID uuid.UUID) bool {
	for _, roleID := range m.memberRoles[memberID] {
		if role, ok := m.roles[roleID]; ok && role.Key == string(authz.RoleOwner) {
			return true
		}
	}

	return false
}
