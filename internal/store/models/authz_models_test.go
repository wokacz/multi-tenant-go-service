package models_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

func TestOrganizationBeforeSaveRejectsAMalformedSlug(t *testing.T) {
	tests := map[string]bool{
		"acme":                         true,
		"acme-labs":                    true,
		"a":                            true,
		"acme2":                        true,
		models.DefaultOrganizationSlug: true,

		"":           false,
		"Acme":       false, // uppercase would make two slugs look like one
		"acme labs":  false,
		"acme--labs": false,
		"-acme":      false,
		"acme-":      false,
		"acme_labs":  false,
		"acme.labs":  false,
		strings.Repeat("a", models.MaxSlugLength+1): false,
	}

	for slug, want := range tests {
		t.Run(slug, func(t *testing.T) {
			org := &models.Organization{Slug: slug, Name: "Acme"}

			err := org.BeforeSave(nil)
			if got := err == nil; got != want {
				t.Errorf("BeforeSave() with slug %q = %v, want accepted = %v", slug, err, want)
			}
		})
	}
}

func TestOrganizationBeforeSaveRequiresAName(t *testing.T) {
	org := &models.Organization{Slug: "acme"}

	if err := org.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for an empty name")
	}
}

// TestDefaultOrganizationCanBeProtected covers the reuse of SoftDelete for the
// one organization that must survive. An installation whose only organization
// was deleted has no working accounts and no screen to undo it from.
func TestDefaultOrganizationCanBeProtected(t *testing.T) {
	org := &models.Organization{Slug: models.DefaultOrganizationSlug, Name: "Default"}
	org.IsProtected = true

	if err := org.BeforeDelete(nil); !errors.Is(err, models.ErrProtected) {
		t.Errorf("BeforeDelete() on the default organization = %v, want ErrProtected", err)
	}
}

func TestMembershipStatusOnlyGrantsWhenActive(t *testing.T) {
	tests := map[models.MembershipStatus]struct{ valid, grants bool }{
		models.MembershipActive:    {true, true},
		models.MembershipInvited:   {true, false},
		models.MembershipSuspended: {true, false},
		"":                         {false, false},
		"member":                   {false, false},
	}

	for status, want := range tests {
		t.Run(string(status), func(t *testing.T) {
			if got := status.Valid(); got != want.valid {
				t.Errorf("%q.Valid() = %v, want %v", status, got, want.valid)
			}

			if got := status.GrantsPermissions(); got != want.grants {
				t.Errorf("%q.GrantsPermissions() = %v, want %v", status, got, want.grants)
			}
		})
	}
}

func TestMembershipBeforeSaveRejectsAnUnknownStatus(t *testing.T) {
	m := &models.Membership{Status: "member"}
	if err := m.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for an unknown status")
	}

	m.Status = models.MembershipActive
	if err := m.BeforeSave(nil); err != nil {
		t.Errorf("BeforeSave() = %v, want nil", err)
	}
}

// TestActivateStampsJoinedAtOnlyOnce is what keeps "reinstated last Tuesday"
// from overwriting "joined three years ago".
func TestActivateStampsJoinedAtOnlyOnce(t *testing.T) {
	joined := time.Date(2020, time.March, 1, 12, 0, 0, 0, time.UTC)

	m := &models.Membership{Status: models.MembershipInvited}
	m.Activate(joined)

	if m.JoinedAt == nil || !m.JoinedAt.Equal(joined) {
		t.Fatalf("Activate() left JoinedAt = %v, want %v", m.JoinedAt, joined)
	}

	m.Suspend()
	if m.IsActive() {
		t.Error("Suspend() left the membership active")
	}

	m.Activate(joined.Add(24 * time.Hour))

	if !m.JoinedAt.Equal(joined) {
		t.Errorf("re-activating rewrote JoinedAt to %v, want %v", m.JoinedAt, joined)
	}

	if !m.IsActive() {
		t.Error("Activate() did not restore the membership")
	}
}

func TestRoleBeforeSaveRejectsAMalformedKey(t *testing.T) {
	tests := map[string]bool{
		"owner":          true,
		"platform_admin": true,
		"level2":         true,

		"":          false,
		"Owner":     false,
		"co owner":  false,
		"co-owner":  false,
		"co__owner": false,
		"_owner":    false,
		"owner_":    false,
		strings.Repeat("a", models.MaxRoleKeyLength+1): false,
	}

	for key, want := range tests {
		t.Run(key, func(t *testing.T) {
			role := &models.Role{Key: key, Name: "Role"}

			err := role.BeforeSave(nil)
			if got := err == nil; got != want {
				t.Errorf("BeforeSave() with key %q = %v, want accepted = %v", key, err, want)
			}
		})
	}
}

// TestSystemRolesRefuseDeletion is the model-level half of the protection. The
// service checks it too, but a hook runs for every delete, including one written
// as a bare db.Delete that never passed through the service.
func TestSystemRolesRefuseDeletion(t *testing.T) {
	role := &models.Role{Key: "admin", Name: "Administrator", IsSystem: true}
	role.ID = uuid.Must(uuid.NewV7())

	if err := role.BeforeDelete(nil); !errors.Is(err, models.ErrRoleIsSystem) {
		t.Errorf("BeforeDelete() on a system role = %v, want ErrRoleIsSystem", err)
	}

	role.IsSystem = false
	if err := role.BeforeDelete(nil); err != nil {
		t.Errorf("BeforeDelete() on a custom role = %v, want nil", err)
	}
}

// TestRoleBatchDeleteIsRefused closes the way around the check above: a batch
// delete hands the hook a zero-valued receiver, so IsSystem reads false and
// every system role in the table would go.
func TestRoleBatchDeleteIsRefused(t *testing.T) {
	role := &models.Role{Key: "admin", Name: "Administrator", IsSystem: true}

	if err := role.BeforeDelete(nil); !errors.Is(err, models.ErrRoleBatchDeleteUnsupported) {
		t.Errorf("BeforeDelete() without a primary key = %v, want ErrRoleBatchDeleteUnsupported", err)
	}
}

func TestUserSystemRoleBeforeSaveValidatesTheKey(t *testing.T) {
	r := &models.UserSystemRole{RoleKey: "Platform Admin"}
	if err := r.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for a malformed key")
	}

	r.RoleKey = "platform_admin"
	if err := r.BeforeSave(nil); err != nil {
		t.Errorf("BeforeSave() = %v, want nil", err)
	}
}

func TestRoleTranslationBeforeSaveRequiresLocaleAndName(t *testing.T) {
	tests := map[string]models.RoleTranslation{
		"no locale":       {Name: "Ksiegowosc"},
		"no name":         {Locale: "pl"},
		"locale too long": {Locale: "pl-PL-x-toolong", Name: "Ksiegowosc"},
	}

	for name, tr := range tests {
		t.Run(name, func(t *testing.T) {
			if err := tr.BeforeSave(nil); err == nil {
				t.Error("BeforeSave() = nil, want an error")
			}
		})
	}

	ok := models.RoleTranslation{Locale: "pl", Name: "Ksiegowosc"}
	if err := ok.BeforeSave(nil); err != nil {
		t.Errorf("BeforeSave() on a valid translation = %v, want nil", err)
	}
}

func TestAuthzEventBeforeSaveRejectsUnknownActions(t *testing.T) {
	e := &models.AuthzEvent{Action: "role.exploded", ActorID: uuid.Must(uuid.NewV7())}
	if err := e.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for an unknown action")
	}

	e.Action = models.ActionRolePermissionsChanged
	if err := e.BeforeSave(nil); err != nil {
		t.Errorf("BeforeSave() = %v, want nil", err)
	}
}

// TestAuthzEventRequiresAnActor keeps the audit trail answering the only
// question it exists for. A row saying a role changed but not who changed it is
// worse than no row, because it looks like coverage.
func TestAuthzEventRequiresAnActor(t *testing.T) {
	e := &models.AuthzEvent{Action: models.ActionRoleCreated}

	if err := e.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for an event with no actor")
	}
}
