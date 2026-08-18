package repositories

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
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
// The deleted_at filters are written out, and they always were: no ORM's soft-delete
// scope reaches a query built from table names. Without them a deleted organization
// keeps granting permissions and a deleted account keeps holding them — the second is
// how a deleted platform administrator would keep getting in.
func (r *Authz) OrganizationPermissionKeys(ctx context.Context, userID, orgID uuid.UUID) ([]string, error) {
	const query = `
		SELECT DISTINCT rp.permission_key
		FROM memberships AS m
		JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		LEFT JOIN membership_roles mr ON mr.membership_id = m.id
		LEFT JOIN roles r ON r.id = mr.role_id
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		WHERE m.user_id = $1 AND m.organization_id = $2 AND m.status = $3`

	keys, rowCount, err := r.permissionKeys(ctx, query, userID, orgID, models.MembershipActive)
	if err != nil {
		return nil, fmt.Errorf("store: organization permission keys: %w", err)
	}

	// No rows at all means no active membership. The organization may not exist, may be
	// deleted, or may simply not have this person in it; the caller must not be able to
	// tell which, so they share one error.
	if rowCount == 0 {
		return nil, authz.ErrNotMember
	}

	return keys, nil
}

// permissionKeys runs a query returning one nullable key per row.
//
// It hands back the row count separately, because "no rows" and "rows whose key is
// NULL" are different answers here and the caller decides what each means.
func (r *Authz) permissionKeys(ctx context.Context, query string, args ...any) ([]string, int, error) {
	rows, err := r.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}

	defer func() {
		_ = rows.Close()
	}()

	var (
		keys  []string
		count int
	)

	for rows.Next() {
		var key sql.NullString
		if err := rows.Scan(&key); err != nil {
			return nil, 0, err
		}

		count++

		if key.Valid {
			keys = append(keys, key.String)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return keys, count, nil
}

// PermissionKeysByOrganization is OrganizationPermissionKeys without the
// organization filter, folded into a map.
//
// The inner join here is deliberate where the other query uses LEFT JOINs: this
// one only feeds the snapshot, and an organization the caller holds nothing in
// is one the UI has nothing to unlock. The other query has to tell a member
// with no roles apart from a stranger, because those become different statuses.
func (r *Authz) PermissionKeysByOrganization(ctx context.Context, userID uuid.UUID) (map[uuid.UUID][]string, error) {
	const query = `
		SELECT DISTINCT m.organization_id, rp.permission_key
		FROM memberships AS m
		JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		JOIN membership_roles mr ON mr.membership_id = m.id
		JOIN roles r ON r.id = mr.role_id
		JOIN role_permissions rp ON rp.role_id = r.id
		WHERE m.user_id = $1 AND m.status = $2`

	rows, err := r.db.SQL().QueryContext(ctx, query, userID, models.MembershipActive)
	if err != nil {
		return nil, fmt.Errorf("store: permission keys by organization: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	out := map[uuid.UUID][]string{}

	for rows.Next() {
		var (
			orgID uuid.UUID
			key   string
		)

		if err := rows.Scan(&orgID, &key); err != nil {
			return nil, fmt.Errorf("store: permission keys by organization: scan: %w", err)
		}

		out[orgID] = append(out[orgID], key)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: permission keys by organization: %w", err)
	}

	return out, nil
}

// SystemRoleKeys lists the installation-wide roles granted to the user.
//
// Keys are returned raw. Which of them this build still ships is a question for
// the catalog, and answering it here would put a copy of that list in the store.
func (r *Authz) SystemRoleKeys(ctx context.Context, userID uuid.UUID) ([]string, error) {
	const query = `
		SELECT usr.role_key
		FROM user_system_roles AS usr
		JOIN users u ON u.id = usr.user_id AND u.deleted_at IS NULL
		WHERE usr.user_id = $1`

	rows, err := r.db.SQL().QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("store: system role keys: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	var keys []string

	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("store: system role keys: scan: %w", err)
		}

		keys = append(keys, key)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: system role keys: %w", err)
	}

	return keys, nil
}
