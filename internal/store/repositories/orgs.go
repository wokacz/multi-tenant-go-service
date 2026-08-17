package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// Orgs implements orgs.Repository and orgs.Directory.
type Orgs struct {
	db *store.DB
}

func NewOrgs(db *store.DB) *Orgs {
	return &Orgs{db: db}
}

var (
	_ orgs.Repository  = (*Orgs)(nil)
	_ orgs.Directory   = (*Orgs)(nil)
	_ orgs.Provisioner = (*Orgs)(nil)
)

// translateOrgError funnels the driver errors this file produces into domain
// vocabulary. GORM's error types stop here, which is what lets internal/api map
// orgs.ErrNotFound onto a 404 without knowing a database was involved.
func translateOrgError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return orgs.ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return orgs.ErrRoleKeyTaken
	case errors.Is(err, models.ErrProtected),
		errors.Is(err, models.ErrRoleIsSystem),
		errors.Is(err, orgs.ErrRoleProtected),
		errors.Is(err, orgs.ErrAlreadyMember),
		errors.Is(err, orgs.ErrLastOwner),
		errors.Is(err, orgs.ErrNotFound):
		return err
	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}

func (r *Orgs) Organization(ctx context.Context, orgID uuid.UUID) (*models.Organization, error) {
	var org models.Organization

	// Queried through the model, so GORM's soft-delete scope applies and a
	// deleted organization is already excluded.
	if err := r.db.WithContext(ctx).First(&org, "id = ?", orgID).Error; err != nil {
		return nil, translateOrgError("organization", err)
	}

	return &org, nil
}

// UpdateOrganization renames it with a targeted statement.
//
// Hooks are skipped, and that needs saying. GORM runs BeforeSave against the
// struct handed to Model, which for a statement-level update is a zero value —
// so Organization.BeforeSave would see an empty slug and refuse a rename that is
// perfectly valid. The validation that matters happens twice anyway: the service
// checks the name before calling, and NOT NULL plus the length limit are on the
// column. The same applies to UpdateRole and SetMemberStatus below.
func (r *Orgs) UpdateOrganization(ctx context.Context, orgID uuid.UUID, name string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Session(&gorm.Session{SkipHooks: true}).
			Model(&models.Organization{}).
			Where("id = ?", orgID).
			Update("name", name)
		if res.Error != nil {
			return res.Error
		}

		if res.RowsAffected == 0 {
			return orgs.ErrNotFound
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionOrganizationUpdated,
			Detail:         name,
		})
	})

	return translateOrgError("update organization", err)
}

// DeleteOrganization soft deletes it. The row has to be loaded first so
// BeforeDelete sees IsProtected — a bare Where(...).Delete() hands the hook a
// zero-valued receiver and the protection would silently not apply.
func (r *Orgs) DeleteOrganization(ctx context.Context, orgID uuid.UUID) error {
	org, err := r.Organization(ctx, orgID)
	if err != nil {
		return err
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(org).Error; err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionOrganizationDeleted,
			Detail:         org.Slug,
		})
	})

	return translateOrgError("delete organization", err)
}

// memberRow is the flat shape the member queries scan into.
type memberRow struct {
	ID       uuid.UUID
	UserID   *uuid.UUID
	Name     string
	Email    string
	Status   models.MembershipStatus
	JoinedAt *time.Time
}

// The join has to be a left one, because an invitation carries no user id and
// still has to appear. That is also the trap: a condition in a LEFT JOIN does
// not remove rows, it only blanks the joined columns, so "AND u.deleted_at IS
// NULL" on its own filtered nothing and a deleted account stayed on the list
// with an empty name. The predicate that actually drops it belongs in the WHERE
// clause: a row either has no account at all, or has one that the join matched.
const liveAccountOrInvitation = "(m.user_id IS NULL OR u.id IS NOT NULL)"

func (r *Orgs) Members(ctx context.Context, orgID uuid.UUID) ([]orgs.Member, error) {
	var rows []memberRow

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("m.id, m.user_id, COALESCE(u.name, '') AS name, COALESCE(u.email, m.email) AS email, m.status, m.joined_at").
		Joins("LEFT JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.organization_id = ? AND "+liveAccountOrInvitation, orgID).
		Order("COALESCE(u.name, m.email) ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: members: %w", err)
	}

	return r.attachRoles(ctx, rows)
}

func (r *Orgs) Member(ctx context.Context, orgID, memberID uuid.UUID) (*orgs.Member, error) {
	var rows []memberRow

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("m.id, m.user_id, COALESCE(u.name, '') AS name, COALESCE(u.email, m.email) AS email, m.status, m.joined_at").
		Joins("LEFT JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.organization_id = ? AND m.id = ? AND "+liveAccountOrInvitation, orgID, memberID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: member: %w", err)
	}

	// Scoped by organization, so a membership id from another tenant simply
	// does not match and comes back as not found. A membership whose account has
	// been deleted is not found either, the same as in Members.
	if len(rows) == 0 {
		return nil, orgs.ErrNotFound
	}

	members, err := r.attachRoles(ctx, rows)
	if err != nil {
		return nil, err
	}

	return &members[0], nil
}

// attachRoles fills in each member's roles with one further query rather than
// one per member.
func (r *Orgs) attachRoles(ctx context.Context, rows []memberRow) ([]orgs.Member, error) {
	out := make([]orgs.Member, 0, len(rows))
	for _, row := range rows {
		member := orgs.Member{
			ID:       row.ID,
			Name:     row.Name,
			Email:    row.Email,
			Status:   row.Status,
			JoinedAt: row.JoinedAt,
			Roles:    []orgs.RoleSummary{},
		}
		if row.UserID != nil {
			member.UserID = *row.UserID
		}

		out = append(out, member)
	}

	if len(out) == 0 {
		return out, nil
	}

	ids := make([]uuid.UUID, 0, len(out))
	for _, member := range out {
		ids = append(ids, member.ID)
	}

	var assignments []struct {
		MembershipID uuid.UUID
		ID           uuid.UUID
		Key          string
		Name         string
		IsSystem     bool
	}

	err := r.db.WithContext(ctx).
		Table("membership_roles AS mr").
		Select("mr.membership_id, r.id, r.key, r.name, r.is_system").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Where("mr.membership_id IN ?", ids).
		Order("r.key ASC").
		Scan(&assignments).Error
	if err != nil {
		return nil, fmt.Errorf("store: member roles: %w", err)
	}

	index := make(map[uuid.UUID]int, len(out))
	for i, member := range out {
		index[member.ID] = i
	}

	for _, a := range assignments {
		i, ok := index[a.MembershipID]
		if !ok {
			continue
		}

		out[i].Roles = append(out[i].Roles, orgs.RoleSummary{
			ID: a.ID, Key: a.Key, Name: a.Name, IsSystem: a.IsSystem,
		})
	}

	return out, nil
}

func (r *Orgs) AddMember(
	ctx context.Context,
	orgID, userID uuid.UUID,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	at time.Time,
) (*orgs.Member, error) {
	var account models.User
	if err := r.db.WithContext(ctx).Select("id", "email").
		First(&account, "id = ?", userID).Error; err != nil {
		return nil, translateOrgError("add member", err)
	}

	uid := userID
	membership := &models.Membership{
		OrganizationID: orgID,
		UserID:         &uid,
		Email:          account.Email,
		Status:         models.MembershipActive,
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		membership.InvitedBy = &by
	}

	membership.Activate(at)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		action := models.ActionMemberInvited

		// A unique violation aborts the Postgres transaction. The savepoint
		// lets us recover and claim an outstanding invitation in the same
		// transaction instead of returning 25P02 from the follow-up SELECT.
		if err := tx.SavePoint("add_member").Error; err != nil {
			return err
		}

		if err := tx.Create(membership).Error; err != nil {
			if !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}

			if err := tx.RollbackTo("add_member").Error; err != nil {
				return err
			}

			claimed, claimErr := claimInvitation(tx, orgID, userID, account.Email, at)
			if claimErr != nil {
				return claimErr
			}

			membership = claimed
			action = models.ActionMemberAccepted
		}

		if err := tx.Where("membership_id = ?", membership.ID).
			Delete(&models.MembershipRole{}).Error; err != nil {
			return err
		}

		if err := assignRoles(tx, orgID, membership.ID, roleIDs); err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			SubjectID:      &userID,
			Action:         action,
		})
	})
	if err != nil {
		return nil, translateOrgError("add member", err)
	}

	return r.Member(ctx, orgID, membership.ID)
}

func (r *Orgs) InviteMember(
	ctx context.Context,
	orgID uuid.UUID,
	email string,
	roleIDs []uuid.UUID,
	invitedBy uuid.UUID,
	at time.Time,
) (*orgs.Member, error) {
	membership := &models.Membership{
		OrganizationID: orgID,
		Email:          email,
		Status:         models.MembershipInvited,
	}

	if !at.IsZero() {
		membership.CreatedAt = at.UTC()
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		membership.InvitedBy = &by
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(membership).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return orgs.ErrAlreadyMember
			}

			return err
		}

		if err := assignRoles(tx, orgID, membership.ID, roleIDs); err != nil {
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

	return r.Member(ctx, orgID, membership.ID)
}

func (r *Orgs) SetMemberStatus(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	status models.MembershipStatus,
	at time.Time,
) error {
	updates := map[string]any{"status": status}

	// Reinstating somebody who never accepted stamps the join date; one who
	// already has one keeps it, so "joined three years ago" is not rewritten to
	// "joined on Tuesday".
	if status.GrantsPermissions() {
		updates["joined_at"] = gorm.Expr("COALESCE(joined_at, ?::timestamptz)", at.UTC())
	}

	action := models.ActionMemberSuspended
	if status.GrantsPermissions() {
		action = models.ActionMemberReinstated
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := refuseLastOwnerLoss(tx, orgID, memberID, !status.GrantsPermissions()); err != nil {
			return err
		}

		res := tx.Session(&gorm.Session{SkipHooks: true}).
			Model(&models.Membership{}).
			Where("id = ? AND organization_id = ?", memberID, orgID).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}

		if res.RowsAffected == 0 {
			return orgs.ErrNotFound
		}

		return recordAboutMember(ctx, tx, orgID, memberID, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         action,
			Detail:         string(status),
		})
	})

	return translateOrgError("set member status", err)
}

func (r *Orgs) RemoveMember(ctx context.Context, orgID, memberID uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := refuseLastOwnerLoss(tx, orgID, memberID, true); err != nil {
			return err
		}

		// The subject is read before the row goes, or there is nothing left to
		// attribute the entry to.
		event := &models.AuthzEvent{OrganizationID: &orgID, Action: models.ActionMemberRemoved}
		if err := recordAboutMember(ctx, tx, orgID, memberID, event); err != nil {
			return err
		}

		res := tx.Where("id = ? AND organization_id = ?", memberID, orgID).
			Delete(&models.Membership{})
		if res.Error != nil {
			return res.Error
		}

		if res.RowsAffected == 0 {
			return orgs.ErrNotFound
		}

		return nil
	})

	return translateOrgError("remove member", err)
}

func (r *Orgs) ReplaceMemberRoles(ctx context.Context, orgID, memberID uuid.UUID, roleIDs []uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		losingOwner, err := replacingRolesDropsOwner(tx, orgID, roleIDs)
		if err != nil {
			return err
		}

		if err := refuseLastOwnerLoss(tx, orgID, memberID, losingOwner); err != nil {
			return err
		}

		var count int64
		if err := tx.Model(&models.Membership{}).
			Where("id = ? AND organization_id = ?", memberID, orgID).
			Count(&count).Error; err != nil {
			return err
		}

		if count == 0 {
			return orgs.ErrNotFound
		}

		if err := tx.Where("membership_id = ?", memberID).Delete(&models.MembershipRole{}).Error; err != nil {
			return err
		}

		if err := assignRoles(tx, orgID, memberID, roleIDs); err != nil {
			return err
		}

		return recordAboutMember(ctx, tx, orgID, memberID, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionMemberRolesChanged,
		})
	})

	return translateOrgError("replace member roles", err)
}

// assignRoles inserts the assignments, refusing any role that is not this
// organization's.
//
// The check is a filtered count rather than a foreign key, because the foreign
// key only says the role exists somewhere. Borrowing another tenant's role id
// would otherwise be a perfectly valid insert.
func assignRoles(tx *gorm.DB, orgID, membershipID uuid.UUID, roleIDs []uuid.UUID) error {
	if len(roleIDs) == 0 {
		return nil
	}

	var owned int64
	if err := tx.Model(&models.Role{}).
		Where("organization_id = ? AND id IN ?", orgID, roleIDs).
		Distinct("id").
		Count(&owned).Error; err != nil {
		return err
	}

	if int(owned) != len(uniqueIDs(roleIDs)) {
		return orgs.ErrNotFound
	}

	rows := make([]models.MembershipRole, 0, len(roleIDs))
	for _, roleID := range uniqueIDs(roleIDs) {
		rows = append(rows, models.MembershipRole{MembershipID: membershipID, RoleID: roleID})
	}

	return tx.Create(&rows).Error
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))

	for _, id := range ids {
		if !containsID(out, id) {
			out = append(out, id)
		}
	}

	return out
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}

	return false
}

func (r *Orgs) Roles(ctx context.Context, orgID uuid.UUID) ([]orgs.Role, error) {
	var rows []models.Role
	if err := r.db.WithContext(ctx).
		Where("organization_id = ?", orgID).
		Order("is_system DESC, key ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: roles: %w", err)
	}

	return r.decorateRoles(ctx, rows)
}

func (r *Orgs) Role(ctx context.Context, orgID, roleID uuid.UUID) (*orgs.Role, error) {
	var row models.Role

	err := r.db.WithContext(ctx).
		First(&row, "id = ? AND organization_id = ?", roleID, orgID).Error
	if err != nil {
		return nil, translateOrgError("role", err)
	}

	decorated, err := r.decorateRoles(ctx, []models.Role{row})
	if err != nil {
		return nil, err
	}

	return &decorated[0], nil
}

// decorateRoles attaches permissions and holder counts with two queries rather
// than two per role.
func (r *Orgs) decorateRoles(ctx context.Context, rows []models.Role) ([]orgs.Role, error) {
	out := make([]orgs.Role, 0, len(rows))
	for _, row := range rows {
		out = append(out, orgs.Role{Role: row, Permissions: []authz.Permission{}})
	}

	if len(out) == 0 {
		return out, nil
	}

	ids := make([]uuid.UUID, 0, len(out))
	for _, role := range out {
		ids = append(ids, role.ID)
	}

	index := make(map[uuid.UUID]int, len(out))
	for i, role := range out {
		index[role.ID] = i
	}

	var permissions []struct {
		RoleID        uuid.UUID
		PermissionKey string
	}

	if err := r.db.WithContext(ctx).
		Table("role_permissions").
		Select("role_id, permission_key").
		Where("role_id IN ?", ids).
		Order("permission_key ASC").
		Scan(&permissions).Error; err != nil {
		return nil, fmt.Errorf("store: role permissions: %w", err)
	}

	for _, row := range permissions {
		i, ok := index[row.RoleID]
		if !ok {
			continue
		}

		// Sanitize is not applied here: the store reports what is stored, and
		// which keys this build still defines is the catalog's question. The
		// settings screen needs to see a stale key to be able to remove it.
		out[i].Permissions = append(out[i].Permissions, authz.Permission(row.PermissionKey))
	}

	var counts []struct {
		RoleID uuid.UUID
		Total  int
	}

	if err := r.db.WithContext(ctx).
		Table("membership_roles").
		Select("role_id, COUNT(*) AS total").
		Where("role_id IN ?", ids).
		Group("role_id").
		Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("store: role holders: %w", err)
	}

	for _, row := range counts {
		if i, ok := index[row.RoleID]; ok {
			out[i].Members = row.Total
		}
	}

	return out, nil
}

func (r *Orgs) CreateRole(
	ctx context.Context,
	orgID uuid.UUID,
	role *models.Role,
	permissions []authz.Permission,
) (*orgs.Role, error) {
	role.OrganizationID = orgID

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(role).Error; err != nil {
			return err
		}

		if err := replacePermissions(tx, role.ID, permissions); err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionRoleCreated,
			RoleID:         &role.ID,
			Detail:         role.Key,
		})
	})
	if err != nil {
		return nil, translateOrgError("create role", err)
	}

	return r.Role(ctx, orgID, role.ID)
}

func (r *Orgs) UpdateRole(ctx context.Context, orgID, roleID uuid.UUID, name, description string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Session(&gorm.Session{SkipHooks: true}).
			Model(&models.Role{}).
			Where("id = ? AND organization_id = ? AND is_system = false", roleID, orgID).
			Updates(map[string]any{"name": name, "description": description})
		if res.Error != nil {
			return res.Error
		}

		if res.RowsAffected == 0 {
			return orgs.ErrNotFound
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionRoleUpdated,
			RoleID:         &roleID,
			Detail:         name,
		})
	})
	if err != nil {
		return translateOrgError("update role", err)
	}

	return nil
}

// DeleteRole loads the row first so BeforeDelete sees IsSystem — a bare
// Where(...).Delete() hands the hook a zero-valued receiver and the protection
// would not apply.
func (r *Orgs) DeleteRole(ctx context.Context, orgID, roleID uuid.UUID) error {
	var role models.Role

	err := r.db.WithContext(ctx).
		First(&role, "id = ? AND organization_id = ?", roleID, orgID).Error
	if err != nil {
		return translateOrgError("delete role", err)
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Recorded before the delete: the role id is captured while the row is
		// still there, and the key with it, so an entry about a role that no
		// longer exists still says which one.
		if err := record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionRoleDeleted,
			RoleID:         &roleID,
			Detail:         role.Key,
		}); err != nil {
			return err
		}

		return tx.Delete(&role).Error
	})

	return translateOrgError("delete role", err)
}

func (r *Orgs) ReplaceRolePermissions(
	ctx context.Context,
	orgID, roleID uuid.UUID,
	permissions []authz.Permission,
) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.Role{}).
			Where("id = ? AND organization_id = ?", roleID, orgID).
			Count(&count).Error; err != nil {
			return err
		}

		if count == 0 {
			return orgs.ErrNotFound
		}

		if err := tx.Where("role_id = ?", roleID).Delete(&models.RolePermission{}).Error; err != nil {
			return err
		}

		if err := replacePermissions(tx, roleID, permissions); err != nil {
			return err
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &orgID,
			Action:         models.ActionRolePermissionsChanged,
			RoleID:         &roleID,
			Detail:         fmt.Sprintf("%d permissions", len(permissions)),
		})
	})

	return translateOrgError("replace role permissions", err)
}

func replacePermissions(tx *gorm.DB, roleID uuid.UUID, permissions []authz.Permission) error {
	if len(permissions) == 0 {
		return nil
	}

	rows := make([]models.RolePermission, 0, len(permissions))
	for _, perm := range permissions {
		rows = append(rows, models.RolePermission{RoleID: roleID, PermissionKey: string(perm)})
	}

	return tx.Create(&rows).Error
}

// OwnerCount counts the active members holding the owner role. Only active
// ones: a suspended owner cannot administer anything, so counting them would
// let the last usable owner be removed.
func (r *Orgs) OwnerCount(ctx context.Context, orgID uuid.UUID) (int, error) {
	var total int64

	err := r.db.WithContext(ctx).
		Table("membership_roles AS mr").
		Joins("JOIN memberships m ON m.id = mr.membership_id").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.organization_id = ? AND m.status = ? AND r.key = ?",
			orgID, models.MembershipActive, string(authz.RoleOwner)).
		Count(&total).Error
	if err != nil {
		return 0, fmt.Errorf("store: owner count: %w", err)
	}

	return int(total), nil
}

// MembershipsForUser lists the account's organizations with the roles it holds
// in each.
//
// Two queries rather than one join: the join would repeat every organization
// once per role and the rows would have to be folded back together in Go, which
// is the shape that quietly turns into an N+1 the first time somebody adds a
// field to it. The second query is a single IN over the memberships already
// found.
func (r *Orgs) MembershipsForUser(ctx context.Context, userID uuid.UUID) ([]orgs.Membership, error) {
	var rows []struct {
		MembershipID uuid.UUID
		Status       models.MembershipStatus
		models.Organization
	}

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("m.id AS membership_id, m.status, o.*").
		Joins("JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL").
		Where("m.user_id = ? OR (m.status = ? AND m.user_id IS NULL AND m.email = (SELECT email FROM users WHERE id = ? AND deleted_at IS NULL))",
			userID, models.MembershipInvited, userID).
		Order("o.name ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: memberships for user: %w", err)
	}

	if len(rows) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MembershipID)
	}

	var assignments []struct {
		MembershipID uuid.UUID
		Key          string
	}

	err = r.db.WithContext(ctx).
		Table("membership_roles AS mr").
		Select("mr.membership_id, r.key").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Where("mr.membership_id IN ?", ids).
		Order("r.key ASC").
		Scan(&assignments).Error
	if err != nil {
		return nil, fmt.Errorf("store: membership roles: %w", err)
	}

	keysByMembership := make(map[uuid.UUID][]string, len(ids))
	for _, a := range assignments {
		keysByMembership[a.MembershipID] = append(keysByMembership[a.MembershipID], a.Key)
	}

	out := make([]orgs.Membership, 0, len(rows))
	for _, row := range rows {
		out = append(out, orgs.Membership{
			ID:           row.MembershipID,
			Organization: row.Organization,
			Status:       row.Status,
			RoleKeys:     keysByMembership[row.MembershipID],
		})
	}

	return out, nil
}

// --- orgs.Provisioner ---

func (r *Orgs) OrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	var org models.Organization

	if err := r.db.WithContext(ctx).First(&org, "slug = ?", slug).Error; err != nil {
		return nil, translateOrgError("organization by slug", err)
	}

	return &org, nil
}

// CreateOrganization stores the organization and materialises the shipped roles
// into it, in one transaction.
//
// The roles are copied per organization rather than referenced from a shared
// table because an owner has to be able to see exactly what "admin" grants here
// and clone it into something editable. The cost is a backfill when the catalog
// grows; the alternative is a role list nobody can inspect or copy.
func (r *Orgs) CreateOrganization(
	ctx context.Context,
	org *models.Organization,
	roles []authz.RoleDefinition,
) (*models.Organization, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(org).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return orgs.ErrSlugTaken
			}

			return err
		}

		for _, def := range roles {
			role := &models.Role{
				OrganizationID: org.ID,
				Key:            string(def.Key),
				Name:           def.Name,
				Description:    def.Description,
				IsSystem:       true,
			}

			if err := tx.Create(role).Error; err != nil {
				return err
			}

			if err := replacePermissions(tx, role.ID, def.Permissions); err != nil {
				return err
			}
		}

		return record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &org.ID,
			Action:         models.ActionOrganizationCreated,
			Detail:         org.Slug,
		})
	})
	if err != nil {
		if errors.Is(err, orgs.ErrSlugTaken) {
			return nil, err
		}

		return nil, translateOrgError("create organization", err)
	}

	return org, nil
}

// AllOrganizations lists every organization, newest first. UUIDv7 is
// time-ordered, so the primary key is already the creation order.
func (r *Orgs) AllOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error) {
	var out []models.Organization

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("store: all organizations: %w", err)
	}

	return out, nil
}

// GrantSystemRole is idempotent: the only caller is a deployment step that may
// well run again, and reporting a duplicate as a failure would make re-running
// it look broken.
func (r *Orgs) GrantSystemRole(
	ctx context.Context,
	userID uuid.UUID,
	key authz.RoleKey,
	grantedBy uuid.UUID,
) error {
	grant := &models.UserSystemRole{UserID: userID, RoleKey: string(key)}
	if grantedBy != uuid.Nil {
		by := grantedBy
		grant.GrantedBy = &by
	}

	err := r.db.WithContext(ctx).Create(grant).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil
	}

	return translateOrgError("grant system role", err)
}

func (r *Orgs) RoleByKey(ctx context.Context, orgID uuid.UUID, key string) (*orgs.Role, error) {
	var row models.Role

	if err := r.db.WithContext(ctx).
		First(&row, "organization_id = ? AND key = ?", orgID, key).Error; err != nil {
		return nil, translateOrgError("role by key", err)
	}

	decorated, err := r.decorateRoles(ctx, []models.Role{row})
	if err != nil {
		return nil, err
	}

	return &decorated[0], nil
}

func (r *Orgs) MemberByUser(ctx context.Context, orgID, userID uuid.UUID) (*orgs.Member, error) {
	var rows []memberRow

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("m.id, m.user_id, u.name, u.email, m.status, m.joined_at").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.organization_id = ? AND m.user_id = ?", orgID, userID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: member by user: %w", err)
	}

	if len(rows) == 0 {
		return nil, orgs.ErrNotFound
	}

	members, err := r.attachRoles(ctx, rows)
	if err != nil {
		return nil, err
	}

	return &members[0], nil
}

func (r *Orgs) AcceptInvitation(
	ctx context.Context,
	memberID, userID uuid.UUID,
	email string,
	at time.Time,
) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return acceptInvitationTx(ctx, tx, memberID, userID, email, at)
	})

	return translateOrgError("accept invitation", err)
}

func (r *Orgs) DeclineInvitation(ctx context.Context, memberID uuid.UUID, email string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var membership models.Membership
		if err := tx.First(&membership, "id = ? AND status = ? AND email = ?",
			memberID, models.MembershipInvited, email).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return orgs.ErrNotFound
			}

			return err
		}

		if err := record(ctx, tx, &models.AuthzEvent{
			OrganizationID: &membership.OrganizationID,
			Action:         models.ActionMemberRemoved,
			Detail:         email,
		}); err != nil {
			return err
		}

		return tx.Delete(&membership).Error
	})

	return translateOrgError("decline invitation", err)
}

func acceptInvitationTx(
	ctx context.Context,
	tx *gorm.DB,
	memberID, userID uuid.UUID,
	email string,
	at time.Time,
) error {
	var membership models.Membership
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&membership, "id = ? AND status = ? AND email = ?",
			memberID, models.MembershipInvited, email).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return orgs.ErrNotFound
		}

		return err
	}

	uid := userID
	membership.UserID = &uid
	membership.Activate(at)

	if err := tx.Save(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return orgs.ErrAlreadyMember
		}

		return err
	}

	return record(ctx, tx, &models.AuthzEvent{
		OrganizationID: &membership.OrganizationID,
		SubjectID:      &userID,
		Action:         models.ActionMemberAccepted,
	})
}

// claimInvitation activates an outstanding invitation for this address so a
// provisioning AddMember (bootstrap, promoting the first owner) does not bounce
// off the unique email index. A live membership for the same address is still
// ErrAlreadyMember.
//
// It is reached only from paths where an operator is acting out of band. The
// registration path must not claim anything — that would accept an invitation
// on the invitee's behalf and replace its roles — so JoinDefaultOrganization
// checks for an invitation before it calls AddMember.
func claimInvitation(
	tx *gorm.DB,
	orgID, userID uuid.UUID,
	email string,
	at time.Time,
) (*models.Membership, error) {
	var existing models.Membership
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("organization_id = ? AND email = ? AND status = ? AND user_id IS NULL",
			orgID, email, models.MembershipInvited).
		First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, orgs.ErrAlreadyMember
		}

		return nil, err
	}

	uid := userID
	existing.UserID = &uid
	existing.Activate(at)

	if err := tx.Save(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, orgs.ErrAlreadyMember
		}

		return nil, err
	}

	return &existing, nil
}

// refuseLastOwnerLoss serialises last-owner checks with the mutation that
// would take the capability away. The organization row is locked so two
// concurrent demotions cannot both observe owners > 1.
func refuseLastOwnerLoss(tx *gorm.DB, orgID, memberID uuid.UUID, losingCapability bool) error {
	if !losingCapability {
		return nil
	}

	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&models.Organization{}, "id = ?", orgID).Error; err != nil {
		return err
	}

	// The join to users is inner, and matches ownerCountTx exactly. The two
	// queries answer the same question — who is an owner here — and the rule has
	// to be one rule. It was a LEFT JOIN, where the deleted_at condition filtered
	// nothing: a membership whose account had been deleted counted as holding
	// owner here but was invisible to ownerCountTx, so removing that row was
	// refused with ErrLastOwner however many live owners the organization had,
	// and promoting another one moved both numbers together. The row could not be
	// removed at all.
	var holds int64
	err := tx.Table("membership_roles AS mr").
		Joins("JOIN memberships m ON m.id = mr.membership_id").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.id = ? AND m.organization_id = ? AND m.status = ? AND r.key = ?",
			memberID, orgID, models.MembershipActive, string(authz.RoleOwner)).
		Count(&holds).Error
	if err != nil {
		return err
	}

	if holds == 0 {
		return nil
	}

	owners, err := ownerCountTx(tx, orgID)
	if err != nil {
		return err
	}

	if owners <= 1 {
		return orgs.ErrLastOwner
	}

	return nil
}

func ownerCountTx(tx *gorm.DB, orgID uuid.UUID) (int, error) {
	var total int64

	err := tx.Table("membership_roles AS mr").
		Joins("JOIN memberships m ON m.id = mr.membership_id").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Where("m.organization_id = ? AND m.status = ? AND r.key = ?",
			orgID, models.MembershipActive, string(authz.RoleOwner)).
		Count(&total).Error
	if err != nil {
		return 0, err
	}

	return int(total), nil
}

// replacingRolesDropsOwner reports whether the new role list leaves out this
// organization's owner role. It says nothing about who holds it — that is
// refuseLastOwnerLoss's question — so it takes no membership id.
func replacingRolesDropsOwner(tx *gorm.DB, orgID uuid.UUID, roleIDs []uuid.UUID) (bool, error) {
	var owner models.Role
	if err := tx.Where("organization_id = ? AND key = ?", orgID, string(authz.RoleOwner)).
		First(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}

		return false, err
	}

	for _, id := range roleIDs {
		if id == owner.ID {
			return false, nil
		}
	}

	return true, nil
}
