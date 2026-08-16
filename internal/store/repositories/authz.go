package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/domain/authz"
	"github.com/wokacz/go-example/internal/store"
	"github.com/wokacz/go-example/internal/store/models"
)

// Authz implements authz.Repository. The interface it satisfies is declared in
// internal/domain/authz, so the dependency points inwards.
type Authz struct {
	db *store.DB
}

func NewAuthz(db *store.DB) *Authz {
	return &Authz{db: db}
}

var _ authz.Repository = (*Authz)(nil)

// OrganizationPermissionKeys resolves everything the user may do in the
// organization, in one round trip.
//
// The LEFT JOINs are what let a single query answer two questions that a plain
// inner join conflates: "is there an active membership" and "what does it
// grant". An inner join returns nothing both for a stranger and for a member
// who holds no roles yet, and those must produce a 404 and an empty grant
// respectively — collapsing them would either leak the existence of the
// organization or lock its newest member out of the 403 they should be getting.
// So a member with no roles comes back as one row with a NULL key.
//
// The deleted_at filters are written out rather than left to GORM. Its
// soft-delete scope only applies to the model being queried, and this query is
// built from a table name: without them, a deleted organization keeps granting
// permissions and a deleted account keeps holding them.
func (r *Authz) OrganizationPermissionKeys(ctx context.Context, userID, orgID uuid.UUID) ([]string, error) {
	var rows []struct {
		PermissionKey *string
	}

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("DISTINCT rp.permission_key").
		Joins("JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Joins("LEFT JOIN membership_roles mr ON mr.membership_id = m.id").
		Joins("LEFT JOIN roles r ON r.id = mr.role_id").
		Joins("LEFT JOIN role_permissions rp ON rp.role_id = r.id").
		Where("m.user_id = ? AND m.organization_id = ? AND m.status = ?",
			userID, orgID, models.MembershipActive).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: organization permission keys: %w", err)
	}

	// No rows at all means no active membership. The organization may not exist,
	// may be deleted, or may simply not have this person in it; the caller must
	// not be able to tell which, so they share one error.
	if len(rows) == 0 {
		return nil, authz.ErrNotMember
	}

	keys := make([]string, 0, len(rows))

	for _, row := range rows {
		if row.PermissionKey == nil {
			continue
		}

		keys = append(keys, *row.PermissionKey)
	}

	return keys, nil
}

// PermissionKeysByOrganization is OrganizationPermissionKeys without the
// organization filter, folded into a map.
//
// The inner join here is deliberate where the other query uses LEFT JOINs: this
// one only feeds the snapshot, and an organization the caller holds nothing in
// is one the UI has nothing to unlock. The other query has to tell a member
// with no roles apart from a stranger, because those become different statuses.
func (r *Authz) PermissionKeysByOrganization(ctx context.Context, userID uuid.UUID) (map[uuid.UUID][]string, error) {
	var rows []struct {
		OrganizationID uuid.UUID
		PermissionKey  string
	}

	err := r.db.WithContext(ctx).
		Table("memberships AS m").
		Select("DISTINCT m.organization_id, rp.permission_key").
		Joins("JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL").
		Joins("JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Joins("JOIN membership_roles mr ON mr.membership_id = m.id").
		Joins("JOIN roles r ON r.id = mr.role_id").
		Joins("JOIN role_permissions rp ON rp.role_id = r.id").
		Where("m.user_id = ? AND m.status = ?", userID, models.MembershipActive).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: permission keys by organization: %w", err)
	}

	out := map[uuid.UUID][]string{}
	for _, row := range rows {
		out[row.OrganizationID] = append(out[row.OrganizationID], row.PermissionKey)
	}

	return out, nil
}

// SystemRoleKeys lists the installation-wide roles granted to the user.
//
// Keys are returned raw. Which of them this build still ships is a question for
// the catalog, and answering it here would put a copy of that list in the store.
func (r *Authz) SystemRoleKeys(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var keys []string

	err := r.db.WithContext(ctx).
		Table("user_system_roles AS usr").
		Joins("JOIN users u ON u.id = usr.user_id AND u.deleted_at IS NULL").
		Where("usr.user_id = ?", userID).
		Pluck("usr.role_key", &keys).Error
	if err != nil {
		return nil, fmt.Errorf("store: system role keys: %w", err)
	}

	return keys, nil
}
