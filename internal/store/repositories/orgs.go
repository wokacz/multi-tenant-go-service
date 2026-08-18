package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membership"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membershiprole"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/organization"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/predicate"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/role"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/rolepermission"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/usersystemrole"
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
// vocabulary. ent's error types stop here, which is what lets internal/api map
// orgs.ErrNotFound onto a 404 without knowing a database was involved.
func translateOrgError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case isNotFound(err):
		return orgs.ErrNotFound
	case isUniqueViolation(err):
		return orgs.ErrRoleKeyTaken
	case errors.Is(err, ent.ErrProtected),
		errors.Is(err, ent.ErrRoleIsSystem),
		errors.Is(err, orgs.ErrRoleProtected),
		errors.Is(err, orgs.ErrAlreadyMember),
		errors.Is(err, orgs.ErrLastOwner),
		errors.Is(err, orgs.ErrNotFound):
		return err
	default:
		return fmt.Errorf("store: %s: %w", op, err)
	}
}

func (r *Orgs) Organization(ctx context.Context, orgID uuid.UUID) (*ent.Organization, error) {
	row, err := r.db.Ent().Organization.Get(ctx, orgID)
	if err != nil {
		return nil, translateOrgError("organization", err)
	}

	out := *row

	return &out, nil
}

// UpdateOrganization renames it with a targeted statement.
//
// The service checks the name before calling, and NOT NULL plus the length limit
// are on the column. DeletedAtIsNil is written out because the interceptor only
// filters reads — an update against a retired row would otherwise rewrite it.
func (r *Orgs) UpdateOrganization(ctx context.Context, orgID uuid.UUID, name string) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		affected, err := tx.Organization.Update().
			Where(organization.ID(orgID), organization.DeletedAtIsNil()).
			SetName(name).
			Save(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return orgs.ErrNotFound
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionOrganizationUpdated,
			Detail:         name,
		})
	})

	return translateOrgError("update organization", err)
}

// DeleteOrganization soft deletes it. The row has to be loaded first so
// IsProtected is visible — the soft-delete hook receives a predicate, not a row,
// and cannot refuse a protected organization on its own.
func (r *Orgs) DeleteOrganization(ctx context.Context, orgID uuid.UUID) error {
	org, err := r.Organization(ctx, orgID)
	if err != nil {
		return err
	}

	if err := org.RefuseIfProtected(); err != nil {
		return err
	}

	err = r.withTx(ctx, func(tx *ent.Tx) error {
		if err := tx.Organization.DeleteOneID(org.ID).Exec(ctx); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionOrganizationDeleted,
			Detail:         org.Slug,
		})
	})

	return translateOrgError("delete organization", err)
}

func (r *Orgs) Members(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]orgs.Member, error) {
	rows, err := r.memberRows(ctx, &memberPage{limit: limit, offset: offset}, membership.OrganizationID(orgID))
	if err != nil {
		return nil, fmt.Errorf("store: members: %w", err)
	}

	return r.attachRoles(ctx, rows)
}

func (r *Orgs) Member(ctx context.Context, orgID, memberID uuid.UUID) (*orgs.Member, error) {
	rows, err := r.memberRows(ctx, nil, membership.OrganizationID(orgID), membership.ID(memberID))
	if err != nil {
		return nil, fmt.Errorf("store: member: %w", err)
	}

	// Scoped by organization, so a membership id from another tenant simply
	// does not match and comes back as not found. A membership whose account has
	// been deleted is not found either, the same as in Members: HasUser goes
	// through the interceptor, so a retired account is not on the other side of
	// the edge.
	if len(rows) == 0 {
		return nil, orgs.ErrNotFound
	}

	members, err := r.attachRoles(ctx, rows)
	if err != nil {
		return nil, err
	}

	return &members[0], nil
}

// memberRow is the flat shape the member queries scan into.
type memberRow struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Name     string
	Email    string
	Status   ent.MembershipStatus
	JoinedAt *time.Time
}

// memberRows lists memberships whose account is still live.
//
// The join is inner, and now it can be. It had to be a left join while an
// invitation was a membership with no user id, and that was the trap: a condition
// in a LEFT JOIN does not remove rows, it only blanks the joined columns, so
// "AND u.deleted_at IS NULL" filtered nothing and a deleted account stayed on the
// list with an empty name. Every membership has an account behind it now, so the
// rule "a row whose account is gone is not a member" is HasUser itself.
//
// The order carries the membership id as a tiebreaker. Names are not unique, and
// a sort with ties is free to return them in any order it likes between two
// queries — which with an offset means a page boundary that drops one row and
// repeats another. The id makes the order total, so the same table always pages
// the same way.
type memberPage struct {
	limit, offset int
}

// liveUser is the "account is still there" half of every member lookup.
//
// HasUser() only asks whether the foreign key points at a users row. The
// interceptor that hides retired rows runs on User queries, not on that
// EXISTS, so a deleted account still satisfies HasUser — and WithUser then
// returns nil. HasUserWith(DeletedAtIsNil()) is the predicate that actually
// means the account is still there.
func liveUser() predicate.Membership {
	return membership.HasUserWith(entuser.DeletedAtIsNil())
}

func (r *Orgs) memberRows(
	ctx context.Context,
	page *memberPage,
	preds ...predicate.Membership,
) ([]memberRow, error) {
	query := r.db.Ent().Membership.Query().
		Where(append(preds, liveUser())...).
		WithUser().
		Order(membership.ByUserField(entuser.FieldName), ent.Asc(membership.FieldID))
	if page != nil {
		query = query.Limit(page.limit).Offset(page.offset)
	}

	found, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]memberRow, 0, len(found))
	for _, row := range found {
		u := row.Edges.User
		out = append(out, memberRow{
			ID:       row.ID,
			UserID:   row.UserID,
			Name:     u.Name,
			Email:    u.Email,
			Status:   row.Status,
			JoinedAt: row.JoinedAt,
		})
	}

	return out, nil
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
			UserID:   row.UserID,
			Roles:    []orgs.RoleSummary{},
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

	assignments, err := r.db.Ent().MembershipRole.Query().
		Where(membershiprole.MembershipIDIn(ids...)).
		WithRole().
		Order(membershiprole.ByRoleField(role.FieldKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: member roles: %w", err)
	}

	index := make(map[uuid.UUID]int, len(out))
	for i, member := range out {
		index[member.ID] = i
	}

	for _, a := range assignments {
		roleRow := a.Edges.Role
		if roleRow == nil {
			continue
		}

		i, ok := index[a.MembershipID]
		if !ok {
			continue
		}

		out[i].Roles = append(out[i].Roles, orgs.RoleSummary{
			ID: roleRow.ID, Key: roleRow.Key, Name: roleRow.Name, IsSystem: roleRow.IsSystem,
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
	m := &ent.Membership{
		OrganizationID: orgID,
		UserID:         userID,
		Status:         ent.MembershipActive,
	}

	if invitedBy != uuid.Nil {
		by := invitedBy
		m.InvitedBy = &by
	}

	m.Activate(at)

	var membershipID uuid.UUID

	// This used to open a savepoint, attempt the insert, roll back on a unique
	// violation and claim an outstanding invitation instead — because an invitation
	// was a membership row and the index refused the second one. Invitations have
	// their own table now, so provisioning is an insert and a duplicate means what
	// it says.
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		created, err := tx.Membership.Create().
			SetOrganizationID(m.OrganizationID).
			SetUserID(m.UserID).
			SetStatus(m.Status).
			SetNillableInvitedBy(m.InvitedBy).
			SetNillableJoinedAt(m.JoinedAt).
			Save(ctx)
		if err != nil {
			if isUniqueViolation(err) {
				return orgs.ErrAlreadyMember
			}

			// The foreign key is what decides whether the account exists. Left
			// untranslated it arrives as an opaque 500, and this path takes an
			// account id straight from a request — the platform endpoint that
			// appoints an owner.
			if isForeignKeyViolation(err) {
				return orgs.ErrNotFound
			}

			return err
		}

		membershipID = created.ID

		if err := assignMembershipRoles(ctx, tx, orgID, created.ID, roleIDs); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			SubjectID:      &userID,
			Action:         ent.ActionMemberJoined,
		})
	})
	if err != nil {
		return nil, translateOrgError("add member", err)
	}

	return r.Member(ctx, orgID, membershipID)
}

func (r *Orgs) SetMemberStatus(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	status ent.MembershipStatus,
	at time.Time,
	guard orgs.OwnerGuard,
) error {
	action := ent.ActionMemberSuspended
	if status.GrantsPermissions() {
		action = ent.ActionMemberReinstated
	}

	err := r.withTx(ctx, func(tx *ent.Tx) error {
		if err := applyOwnerGuard(ctx, tx, orgID, memberID, guard); err != nil {
			return err
		}

		update := tx.Membership.Update().
			Where(membership.ID(memberID), membership.OrganizationID(orgID)).
			SetStatus(status)

		// Reinstating somebody who never accepted stamps the join date; one who
		// already has one keeps it, so "joined three years ago" is not rewritten to
		// "joined on Tuesday".
		if status.GrantsPermissions() {
			update.Modify(func(u *entsql.UpdateBuilder) {
				u.Set(membership.FieldJoinedAt, entsql.ExprFunc(func(b *entsql.Builder) {
					b.WriteString("COALESCE(").
						Ident(membership.FieldJoinedAt).
						WriteString(", ").
						Arg(at.UTC()).
						WriteString("::timestamptz)")
				}))
			})
		}

		affected, err := update.Save(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return orgs.ErrNotFound
		}

		return recordAboutMemberEnt(ctx, tx, orgID, memberID, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         action,
			Detail:         string(status),
		})
	})

	return translateOrgError("set member status", err)
}

func (r *Orgs) RemoveMember(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	action string,
	guard orgs.OwnerGuard,
) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		if err := applyOwnerGuard(ctx, tx, orgID, memberID, guard); err != nil {
			return err
		}

		// The subject is read before the row goes, or there is nothing left to
		// attribute the entry to. This does not look the member up through users:
		// a membership whose account is gone is still a row that has to be
		// removable, and every other method reports that row as not found.
		event := &ent.AuthzEvent{OrganizationID: &orgID, Action: action}
		if err := recordAboutMemberEnt(ctx, tx, orgID, memberID, event); err != nil {
			return err
		}

		affected, err := tx.Membership.Delete().
			Where(membership.ID(memberID), membership.OrganizationID(orgID)).
			Exec(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return orgs.ErrNotFound
		}

		return nil
	})

	return translateOrgError("remove member", err)
}

func (r *Orgs) ReplaceMemberRoles(
	ctx context.Context,
	orgID, memberID uuid.UUID,
	roleIDs []uuid.UUID,
	guard orgs.OwnerGuard,
) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		if err := applyOwnerGuard(ctx, tx, orgID, memberID, guard); err != nil {
			return err
		}

		n, err := tx.Membership.Query().
			Where(membership.ID(memberID), membership.OrganizationID(orgID)).
			Count(ctx)
		if err != nil {
			return err
		}

		if n == 0 {
			return orgs.ErrNotFound
		}

		if _, err := tx.MembershipRole.Delete().
			Where(membershiprole.MembershipID(memberID)).
			Exec(ctx); err != nil {
			return err
		}

		if err := assignMembershipRoles(ctx, tx, orgID, memberID, roleIDs); err != nil {
			return err
		}

		return recordAboutMemberEnt(ctx, tx, orgID, memberID, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionMemberRolesChanged,
		})
	})

	return translateOrgError("replace member roles", err)
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

// Roles pages by (is_system, key). The shipped roles come first because that is
// the order the settings screen shows them in, and key is unique within an
// organization, so the order is already total and the pages are stable.
func (r *Orgs) Roles(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]orgs.Role, error) {
	found, err := r.db.Ent().Role.Query().
		Where(role.OrganizationID(orgID)).
		Order(ent.Desc(role.FieldIsSystem), ent.Asc(role.FieldKey)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: roles: %w", err)
	}

	rows := make([]ent.Role, 0, len(found))
	for _, row := range found {
		rows = append(rows, *row)
	}

	return r.decorateRoles(ctx, rows)
}

func (r *Orgs) Role(ctx context.Context, orgID, roleID uuid.UUID) (*orgs.Role, error) {
	row, err := r.db.Ent().Role.Query().
		Where(role.ID(roleID), role.OrganizationID(orgID)).
		Only(ctx)
	if err != nil {
		return nil, translateOrgError("role", err)
	}

	decorated, err := r.decorateRoles(ctx, []ent.Role{*row})
	if err != nil {
		return nil, err
	}

	return &decorated[0], nil
}

// decorateRoles attaches permissions and holder counts with two queries rather
// than two per role.
func (r *Orgs) decorateRoles(ctx context.Context, rows []ent.Role) ([]orgs.Role, error) {
	out := make([]orgs.Role, 0, len(rows))
	for _, row := range rows {
		out = append(out, orgs.Role{Role: row, Permissions: []authz.Permission{}})
	}

	if len(out) == 0 {
		return out, nil
	}

	ids := make([]uuid.UUID, 0, len(out))
	for _, roleRow := range out {
		ids = append(ids, roleRow.ID)
	}

	index := make(map[uuid.UUID]int, len(out))
	for i, roleRow := range out {
		index[roleRow.ID] = i
	}

	permissions, err := r.db.Ent().RolePermission.Query().
		Where(rolepermission.RoleIDIn(ids...)).
		Order(ent.Asc(rolepermission.FieldPermissionKey)).
		All(ctx)
	if err != nil {
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

	holders, err := r.db.Ent().MembershipRole.Query().
		Where(membershiprole.RoleIDIn(ids...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: role holders: %w", err)
	}

	for _, row := range holders {
		if i, ok := index[row.RoleID]; ok {
			out[i].Members++
		}
	}

	return out, nil
}

func (r *Orgs) CreateRole(
	ctx context.Context,
	orgID uuid.UUID,
	roleRow *ent.Role,
	permissions []authz.Permission,
) (*orgs.Role, error) {
	roleRow.OrganizationID = orgID

	err := r.withTx(ctx, func(tx *ent.Tx) error {
		create := tx.Role.Create().
			SetOrganizationID(orgID).
			SetKey(roleRow.Key).
			SetName(roleRow.Name).
			SetDescription(roleRow.Description).
			SetIsSystem(roleRow.IsSystem)
		if roleRow.ID != uuid.Nil {
			create = create.SetID(roleRow.ID)
		}

		created, err := create.Save(ctx)
		if err != nil {
			return err
		}

		roleRow.ID = created.ID
		roleRow.CreatedAt = created.CreatedAt
		roleRow.UpdatedAt = created.UpdatedAt

		if err := insertRolePermissions(ctx, tx, created.ID, permissions); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionRoleCreated,
			RoleID:         &roleRow.ID,
			Detail:         roleRow.Key,
		})
	})
	if err != nil {
		return nil, translateOrgError("create role", err)
	}

	return r.Role(ctx, orgID, roleRow.ID)
}

func (r *Orgs) UpdateRole(ctx context.Context, orgID, roleID uuid.UUID, name, description string) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// SetDescription("") must write: an empty description is a value, not an
		// omit. ClearDescription would leave the previous text in place.
		affected, err := tx.Role.Update().
			Where(role.ID(roleID), role.OrganizationID(orgID), role.IsSystem(false)).
			SetName(name).
			SetDescription(description).
			Save(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return orgs.ErrNotFound
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionRoleUpdated,
			RoleID:         &roleID,
			Detail:         name,
		})
	})
	if err != nil {
		return translateOrgError("update role", err)
	}

	return nil
}

// DeleteRole loads the row first so IsSystem is visible — a delete by predicate
// alone would not see the flag, and a shipped role would go.
func (r *Orgs) DeleteRole(ctx context.Context, orgID, roleID uuid.UUID, guard orgs.RoleGuard) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// The same lock every other serialised change here takes, so this and a
		// concurrent role assignment cannot interleave. Counting holders without it
		// answers a question that stops being true before the delete lands.
		if err := lockOrganization(ctx, tx, orgID); err != nil {
			return err
		}

		row, err := tx.Role.Query().
			Where(role.ID(roleID), role.OrganizationID(orgID)).
			Only(ctx)
		if err != nil {
			return err
		}

		loaded := *row
		if err := loaded.RefuseDelete(); err != nil {
			return err
		}

		holders, err := tx.MembershipRole.Query().
			Where(membershiprole.RoleID(roleID)).
			Count(ctx)
		if err != nil {
			return err
		}

		if err := guard(holders); err != nil {
			return err
		}

		// Recorded before the delete: the role id is captured while the row is
		// still there, and the key with it, so an entry about a role that no
		// longer exists still says which one.
		if err := recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionRoleDeleted,
			RoleID:         &roleID,
			Detail:         row.Key,
		}); err != nil {
			return err
		}

		return tx.Role.DeleteOneID(row.ID).Exec(ctx)
	})

	return translateOrgError("delete role", err)
}

func (r *Orgs) ReplaceRolePermissions(
	ctx context.Context,
	orgID, roleID uuid.UUID,
	permissions []authz.Permission,
) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		n, err := tx.Role.Query().
			Where(role.ID(roleID), role.OrganizationID(orgID)).
			Count(ctx)
		if err != nil {
			return err
		}

		if n == 0 {
			return orgs.ErrNotFound
		}

		if _, err := tx.RolePermission.Delete().
			Where(rolepermission.RoleID(roleID)).
			Exec(ctx); err != nil {
			return err
		}

		if err := insertRolePermissions(ctx, tx, roleID, permissions); err != nil {
			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &orgID,
			Action:         ent.ActionRolePermissionsChanged,
			RoleID:         &roleID,
			Detail:         fmt.Sprintf("%d permissions", len(permissions)),
		})
	})

	return translateOrgError("replace role permissions", err)
}

func insertRolePermissions(ctx context.Context, tx *ent.Tx, roleID uuid.UUID, permissions []authz.Permission) error {
	if len(permissions) == 0 {
		return nil
	}

	creates := make([]*ent.RolePermissionCreate, 0, len(permissions))
	for _, perm := range permissions {
		creates = append(creates, tx.RolePermission.Create().
			SetRoleID(roleID).
			SetPermissionKey(string(perm)))
	}

	return tx.RolePermission.CreateBulk(creates...).Exec(ctx)
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
	found, err := r.db.Ent().Membership.Query().
		Where(
			membership.UserID(userID),
			membership.HasOrganizationWith(organization.DeletedAtIsNil()),
		).
		WithOrganization().
		Order(membership.ByOrganizationField(organization.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: memberships for user: %w", err)
	}

	if len(found) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(found))
	for _, row := range found {
		ids = append(ids, row.ID)
	}

	assignments, err := r.db.Ent().MembershipRole.Query().
		Where(membershiprole.MembershipIDIn(ids...)).
		WithRole().
		Order(membershiprole.ByRoleField(role.FieldKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: membership roles: %w", err)
	}

	keysByMembership := make(map[uuid.UUID][]string, len(ids))
	for _, a := range assignments {
		if a.Edges.Role == nil {
			continue
		}

		keysByMembership[a.MembershipID] = append(keysByMembership[a.MembershipID], a.Edges.Role.Key)
	}

	out := make([]orgs.Membership, 0, len(found))
	for _, row := range found {
		out = append(out, orgs.Membership{
			ID:           row.ID,
			Organization: *row.Edges.Organization,
			Status:       row.Status,
			RoleKeys:     keysByMembership[row.ID],
		})
	}

	return out, nil
}

// --- orgs.Provisioner ---

func (r *Orgs) OrganizationBySlug(ctx context.Context, slug string) (*ent.Organization, error) {
	row, err := r.db.Ent().Organization.Query().
		Where(organization.Slug(slug)).
		Only(ctx)
	if err != nil {
		return nil, translateOrgError("organization by slug", err)
	}

	out := *row

	return &out, nil
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
	org *ent.Organization,
	roles []authz.RoleDefinition,
) (*ent.Organization, error) {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		create := tx.Organization.Create().
			SetSlug(org.Slug).
			SetName(org.Name).
			SetIsProtected(org.IsProtected)
		if org.ID != uuid.Nil {
			create = create.SetID(org.ID)
		}

		created, err := create.Save(ctx)
		if err != nil {
			if isUniqueViolation(err) {
				return orgs.ErrSlugTaken
			}

			return err
		}

		org.ID = created.ID
		org.CreatedAt = created.CreatedAt
		org.UpdatedAt = created.UpdatedAt
		org.IsProtected = created.IsProtected

		for _, def := range roles {
			createdRole, err := tx.Role.Create().
				SetOrganizationID(org.ID).
				SetKey(string(def.Key)).
				SetName(def.Name).
				SetDescription(def.Description).
				SetIsSystem(true).
				Save(ctx)
			if err != nil {
				return err
			}

			if err := insertRolePermissions(ctx, tx, createdRole.ID, def.Permissions); err != nil {
				return err
			}
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			OrganizationID: &org.ID,
			Action:         ent.ActionOrganizationCreated,
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
// activeOwners is the definition of "an owner this organization actually has",
// written once so the listing and the last-owner rule cannot drift apart.
//
// A membership counts when it is active, holds the owner role, and has a live
// account behind it. That last condition is the one that bites: soft deleting an
// account leaves the membership row, still holding owner, and ownerStateTx stopped
// counting it — so a listing that counted it would report an owner for an
// organization the guard treats as having none.
const activeOwners = `(
	SELECT COUNT(DISTINCT m.id)
	FROM membership_roles mr
	JOIN memberships m ON m.id = mr.membership_id
	JOIN roles r ON r.id = mr.role_id
	JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
	WHERE m.organization_id = organizations.id
	  AND m.status = 'active'
	  AND r.key = 'owner'
)`

func (r *Orgs) AllOrganizations(
	ctx context.Context,
	filter orgs.OrganizationFilter,
	limit, offset int,
) ([]orgs.OrganizationSummary, error) {
	// Raw SQL, the same shape audit uses: a correlated subquery over four tables
	// is what this listing *is*, and the interceptor's table alias would make
	// `organizations.id` inside activeOwners a guess. deleted_at is written out
	// because no ORM's soft-delete scope reaches a query built from table names.
	query := `
		SELECT organizations.id, organizations.created_at, organizations.updated_at,
		       organizations.deleted_at, organizations.is_protected, organizations.slug,
		       organizations.name, ` + activeOwners + ` AS owners
		FROM organizations
		WHERE organizations.deleted_at IS NULL`

	if filter.WithoutOwner {
		query += ` AND ` + activeOwners + ` = 0`
	}

	query += `
		ORDER BY organizations.id DESC
		LIMIT $1 OFFSET $2`

	rows, err := r.db.SQL().QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: all organizations: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	out := make([]orgs.OrganizationSummary, 0)

	for rows.Next() {
		var (
			org       ent.Organization
			deletedAt sql.NullTime
			owners    int
		)

		if err := rows.Scan(
			&org.ID, &org.CreatedAt, &org.UpdatedAt,
			&deletedAt, &org.IsProtected, &org.Slug, &org.Name, &owners,
		); err != nil {
			return nil, fmt.Errorf("store: all organizations: scan: %w", err)
		}

		if deletedAt.Valid {
			t := deletedAt.Time
			org.DeletedAt = &t
		}

		out = append(out, orgs.OrganizationSummary{Organization: org, Owners: owners})
	}

	if err := rows.Err(); err != nil {
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
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// ON CONFLICT DO NOTHING rather than inserting and recovering from the
		// unique violation. A violation aborts the whole Postgres transaction, so
		// swallowing the error and returning nil asks the driver to commit a
		// transaction that is already dead — "commit unexpectedly resulted in
		// rollback". The old AddMember worked around exactly this with a
		// savepoint; one statement that cannot fail is better than recovering
		// from one that does.
		//
		// ID() uses RETURNING. A conflict returns no row, which is how this tells
		// "already granted" from "just granted" without a second statement that
		// could disagree.
		create := tx.UserSystemRole.Create().
			SetUserID(userID).
			SetRoleKey(string(key)).
			SetNillableGrantedBy(grantedByPtr(grantedBy)).
			OnConflictColumns(usersystemrole.FieldUserID, usersystemrole.FieldRoleKey).
			DoNothing()

		_, err := create.ID(ctx)
		if err != nil {
			if isNotFound(err) || errors.Is(err, sql.ErrNoRows) {
				return nil
			}

			return err
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			SubjectID: &userID,
			Action:    ent.ActionSystemRoleGranted,
			Detail:    string(key),
		})
	})

	return translateOrgError("grant system role", err)
}

func grantedByPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}

	return &id
}

func (r *Orgs) RevokeSystemRole(ctx context.Context, userID uuid.UUID, key authz.RoleKey) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		affected, err := tx.UserSystemRole.Delete().
			Where(usersystemrole.UserID(userID), usersystemrole.RoleKey(string(key))).
			Exec(ctx)
		if err != nil {
			return err
		}

		// Nothing to revoke is not an error, but it is also not an event.
		if affected == 0 {
			return nil
		}

		return recordEnt(ctx, tx, &ent.AuthzEvent{
			SubjectID: &userID,
			Action:    ent.ActionSystemRoleRevoked,
			Detail:    string(key),
		})
	})

	return translateOrgError("revoke system role", err)
}

func (r *Orgs) SystemRoleHolders(ctx context.Context) ([]orgs.SystemRoleHolder, error) {
	found, err := r.db.Ent().UserSystemRole.Query().
		Where(usersystemrole.HasUserWith(entuser.DeletedAtIsNil())).
		WithUser().
		Order(usersystemrole.ByUserField(entuser.FieldName), usersystemrole.ByRoleKey()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: system role holders: %w", err)
	}

	out := make([]orgs.SystemRoleHolder, 0, len(found))
	for _, row := range found {
		u := row.Edges.User
		out = append(out, orgs.SystemRoleHolder{
			UserID:    row.UserID,
			Name:      u.Name,
			Email:     u.Email,
			RoleKey:   row.RoleKey,
			GrantedBy: row.GrantedBy,
			GrantedAt: row.CreatedAt,
		})
	}

	return out, nil
}

func (r *Orgs) RoleByKey(ctx context.Context, orgID uuid.UUID, key string) (*orgs.Role, error) {
	row, err := r.db.Ent().Role.Query().
		Where(role.OrganizationID(orgID), role.Key(key)).
		Only(ctx)
	if err != nil {
		return nil, translateOrgError("role by key", err)
	}

	decorated, err := r.decorateRoles(ctx, []ent.Role{*row})
	if err != nil {
		return nil, err
	}

	return &decorated[0], nil
}

func (r *Orgs) MemberByUser(ctx context.Context, orgID, userID uuid.UUID) (*orgs.Member, error) {
	rows, err := r.memberRows(ctx, nil, membership.OrganizationID(orgID), membership.UserID(userID))
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

func (r *Orgs) MemberPermissions(ctx context.Context, orgID, memberID uuid.UUID) ([]authz.Permission, error) {
	// Scoped by organization for the same reason every other method here is: a
	// membership id from another tenant must answer nothing rather than answer
	// truthfully. Status is not filtered — see the interface for why.
	keys, err := r.db.Ent().RolePermission.Query().
		Where(rolepermission.HasRoleWith(
			role.HasMembershipRolesWith(
				membershiprole.HasMembershipWith(
					membership.ID(memberID),
					membership.OrganizationID(orgID),
				),
			),
		)).
		Unique(true).
		Order(ent.Asc(rolepermission.FieldPermissionKey)).
		Select(rolepermission.FieldPermissionKey).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: member permissions: %w", err)
	}

	out := make([]authz.Permission, 0, len(keys))
	for _, key := range keys {
		out = append(out, authz.Permission(key))
	}

	return out, nil
}

// lockOrganization takes the row lock every serialised change to an organization
// shares.
//
// One lock object for all of them on purpose: last-owner checks and role deletes
// both need to be ordered against concurrent role assignments, and taking two
// different locks in two different orders is how deadlocks are built.
func lockOrganization(ctx context.Context, tx *ent.Tx, orgID uuid.UUID) error {
	_, err := tx.Organization.Query().
		Where(organization.ID(orgID)).
		ForUpdate().
		Only(ctx)

	return err
}

// ownerStateTx assembles the facts the domain's last-owner rule decides from.
//
// Both numbers come from the same join, so they cannot disagree about a
// membership whose account has been deleted — the disagreement that once made such
// a row impossible to remove. liveUser is inner: a membership that outlived its
// account holds nothing. The predicates here are the same definition as
// activeOwners; TestTheOwnerCountAgreesWithTheOwnerRule is what keeps them so.
func ownerStateTx(ctx context.Context, tx *ent.Tx, orgID, memberID uuid.UUID) (orgs.OwnerState, error) {
	ids, err := tx.Membership.Query().
		Where(
			membership.OrganizationID(orgID),
			membership.StatusEQ(membership.StatusActive),
			liveUser(),
			membership.HasRolesWith(membershiprole.HasRoleWith(role.KeyEQ(string(authz.RoleOwner)))),
		).
		IDs(ctx)
	if err != nil {
		return orgs.OwnerState{}, err
	}

	state := orgs.OwnerState{Owners: len(ids)}
	for _, id := range ids {
		if id == memberID {
			state.SubjectHoldsOwner = true

			break
		}
	}

	return state, nil
}

// applyOwnerGuard locks the organization, reads the state and asks the domain.
func applyOwnerGuard(ctx context.Context, tx *ent.Tx, orgID, memberID uuid.UUID, guard orgs.OwnerGuard) error {
	if err := lockOrganization(ctx, tx, orgID); err != nil {
		return err
	}

	state, err := ownerStateTx(ctx, tx, orgID, memberID)
	if err != nil {
		return err
	}

	return guard(state)
}

// recordAboutMemberEnt fills in the subject from a membership before writing.
//
// The audit is about people, and a membership id means nothing to somebody
// reading the history a year later. Resolving it here rather than at read time
// also means the entry survives the membership being deleted, which is exactly
// the case "who removed them" is asked about.
func recordAboutMemberEnt(
	ctx context.Context,
	tx *ent.Tx,
	orgID, memberID uuid.UUID,
	event *ent.AuthzEvent,
) error {
	if audit.ActorFrom(ctx).IsZero() {
		return nil
	}

	row, err := tx.Membership.Query().
		Where(membership.ID(memberID), membership.OrganizationID(orgID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("store: audit subject: %w", err)
	}

	subject := row.UserID
	event.SubjectID = &subject

	return recordEnt(ctx, tx, event)
}
