package orgs

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

// ErrCannotRevokeOwnLastSystemRole refuses the request that would leave the caller
// unable to undo it.
//
// It mirrors the self-protections on suspending and deleting an account. The
// difference is what the caller loses: revoking their own platform_admin takes away
// the permission needed to grant it back, and with no other holder there is nobody
// left who can. Recovering means the bootstrap command and database access, which is
// not a support path an ordinary mistake should lead to.
var ErrCannotRevokeOwnLastSystemRole = errors.New(
	"orgs: revoking this would leave nobody able to grant it back")

// SystemRoleHolders lists who administers the installation.
func (s *Service) SystemRoleHolders(ctx context.Context) ([]SystemRoleHolder, error) {
	return s.provisioner.SystemRoleHolders(ctx)
}

// GrantSystemRole makes somebody an administrator of the installation.
//
// There is no anti-escalation check here, and that is not an omission. The scopes
// are separate: a caller reaching this has been authorized at system scope, and the
// only system role that exists covers every platform permission — so "you may only
// grant what you hold" would compare a set against itself. A second system role
// with a narrower set would change that, and this is where the check would go.
func (s *Service) GrantSystemRole(ctx context.Context, grant *authz.Grant, userID uuid.UUID, key authz.RoleKey) error {
	if !authz.IsSystemScopeRole(key) {
		return ErrInvalidSystemRole
	}

	return s.provisioner.GrantSystemRole(ctx, userID, key, grant.Actor())
}

// RevokeSystemRole takes an installation-wide role back.
func (s *Service) RevokeSystemRole(ctx context.Context, grant *authz.Grant, userID uuid.UUID, key authz.RoleKey) error {
	if !authz.IsSystemScopeRole(key) {
		return ErrInvalidSystemRole
	}

	if userID == grant.Actor() {
		alone, err := s.lastHolderOf(ctx, key, userID)
		if err != nil {
			return err
		}

		if alone {
			return ErrCannotRevokeOwnLastSystemRole
		}
	}

	return s.provisioner.RevokeSystemRole(ctx, userID, key)
}

// lastHolderOf reports whether userID is the only account holding key.
func (s *Service) lastHolderOf(ctx context.Context, key authz.RoleKey, userID uuid.UUID) (bool, error) {
	holders, err := s.provisioner.SystemRoleHolders(ctx)
	if err != nil {
		return false, err
	}

	for i := range holders {
		if holders[i].RoleKey == string(key) && holders[i].UserID != userID {
			return false, nil
		}
	}

	return true, nil
}
