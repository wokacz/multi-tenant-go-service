package ent_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

func TestDeviceTrustLifecycle(t *testing.T) {
	var d ent.Device

	if d.IsTrusted() {
		t.Fatal("a fresh device reports as trusted")
	}

	if err := d.Trust(); err != nil {
		t.Fatalf("Trust() = %v, want nil", err)
	}
	if !d.IsTrusted() {
		t.Error("device is not trusted after Trust()")
	}

	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}
	if d.IsTrusted() {
		t.Error("device is still trusted after Revoke()")
	}
	if !d.IsRevoked() {
		t.Error("device is not revoked after Revoke()")
	}
}

func TestDeviceRevokeIsIdempotentlyRejected(t *testing.T) {
	var d ent.Device

	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}
	if err := d.Revoke(); !errors.Is(err, ent.ErrDeviceRevoked) {
		t.Errorf("second Revoke() = %v, want ErrDeviceRevoked", err)
	}
	if err := d.Trust(); !errors.Is(err, ent.ErrDeviceRevoked) {
		t.Errorf("Trust() on a revoked device = %v, want ErrDeviceRevoked", err)
	}
}

func TestDeviceUnrevokeRestoresTrustabilityNotTrust(t *testing.T) {
	var d ent.Device
	if err := d.Trust(); err != nil {
		t.Fatalf("Trust() = %v, want nil", err)
	}
	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}

	d.Unrevoke()

	if d.IsRevoked() {
		t.Error("device is still revoked after Unrevoke()")
	}
	if d.IsTrusted() {
		t.Error("Unrevoke() silently restored trust; the user must re-confirm")
	}
	if err := d.Trust(); err != nil {
		t.Errorf("Trust() after Unrevoke() = %v, want nil", err)
	}
}

func TestSoftDeleteProtection(t *testing.T) {
	u := &ent.User{IsProtected: true}

	if err := u.Delete(); !errors.Is(err, ent.ErrProtected) {
		t.Errorf("Delete() = %v, want ErrProtected", err)
	}
	if err := u.RefuseIfProtected(); !errors.Is(err, ent.ErrProtected) {
		t.Errorf("RefuseIfProtected() = %v, want ErrProtected", err)
	}
	if u.IsDeleted() {
		t.Error("a protected record was marked deleted")
	}
}

func TestSoftDeleteRoundTrip(t *testing.T) {
	var u ent.User

	if err := u.Delete(); err != nil {
		t.Fatalf("Delete() = %v, want nil", err)
	}
	if !u.IsDeleted() {
		t.Error("record is not marked deleted after Delete()")
	}

	u.Restore()

	if u.IsDeleted() {
		t.Error("record is still marked deleted after Restore()")
	}
	if err := u.RefuseIfProtected(); err != nil {
		t.Errorf("RefuseIfProtected() on an unprotected record = %v, want nil", err)
	}
}

func TestLoginOutcomeValid(t *testing.T) {
	valid := []ent.LoginOutcome{
		ent.OutcomeSuccess,
		ent.OutcomeBadPassword,
		ent.OutcomeMFAFailed,
		ent.OutcomeLocked,
	}
	for _, o := range valid {
		if !o.Valid() {
			t.Errorf("LoginOutcome(%q).Valid() = false, want true", o)
		}
	}

	for _, o := range []ent.LoginOutcome{"", "SUCCESS", "expired"} {
		if o.Valid() {
			t.Errorf("LoginOutcome(%q).Valid() = true, want false", o)
		}
	}
}

func TestLoginEventValidateRejectsUnknownOutcome(t *testing.T) {
	e := ent.LoginEvent{Outcome: "definitely_not_an_outcome"}
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for an unknown outcome")
	}

	e.Outcome = ent.OutcomeSuccess
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestOrganizationValidateRejectsAMalformedSlug(t *testing.T) {
	tests := map[string]bool{
		"acme":                                   true,
		"acme-labs":                              true,
		"a":                                      true,
		"acme2":                                  true,
		ent.DefaultOrganizationSlug:              true,
		"":                                       false,
		"Acme":                                   false,
		"acme labs":                              false,
		"acme--labs":                             false,
		"-acme":                                  false,
		"acme-":                                  false,
		"acme_labs":                              false,
		"acme.labs":                              false,
		strings.Repeat("a", ent.MaxSlugLength+1): false,
	}

	for slug, want := range tests {
		t.Run(slug, func(t *testing.T) {
			org := &ent.Organization{Slug: slug, Name: "Acme"}

			err := org.Validate()
			if got := err == nil; got != want {
				t.Errorf("Validate() with slug %q = %v, want accepted = %v", slug, err, want)
			}
		})
	}
}

func TestOrganizationValidateRequiresAName(t *testing.T) {
	org := &ent.Organization{Slug: "acme"}

	if err := org.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for an empty name")
	}
}

// TestDefaultOrganizationCanBeProtected covers the reuse of soft-delete for the
// one organization that must survive. An installation whose only organization
// was deleted has no working accounts and no screen to undo it from.
func TestDefaultOrganizationCanBeProtected(t *testing.T) {
	org := &ent.Organization{Slug: ent.DefaultOrganizationSlug, Name: "Default", IsProtected: true}

	if err := org.RefuseIfProtected(); !errors.Is(err, ent.ErrProtected) {
		t.Errorf("RefuseIfProtected() on the default organization = %v, want ErrProtected", err)
	}
}

func TestMembershipStatusOnlyGrantsWhenActive(t *testing.T) {
	tests := map[ent.MembershipStatus]struct{ valid, grants bool }{
		ent.MembershipActive:    {true, true},
		ent.MembershipSuspended: {true, false},
		"":                      {false, false},
		"member":                {false, false},
		// "invited" was a status and is not one any more. It stays in the table as
		// a case that must be rejected: a row carrying it is a membership that
		// nobody agreed to, and the enum is what stops one being written.
		"invited": {false, false},
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

func TestMembershipValidateRejectsAnUnknownStatus(t *testing.T) {
	m := &ent.Membership{Status: "member"}
	if err := m.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for an unknown status")
	}

	m.Status = ent.MembershipActive
	m.UserID = uuid.Must(uuid.NewV7())
	if err := m.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestMembershipValidateRequiresAnAccount is the invariant that replaced two
// earlier ones. A membership used to be allowed without an account — that was how
// an invitation was stored — so the rule had to be "an *active* one needs an
// account", and an address column stood in for the missing person. Now every
// membership is somebody.
func TestMembershipValidateRequiresAnAccount(t *testing.T) {
	m := &ent.Membership{Status: ent.MembershipActive}
	if err := m.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a membership with no account")
	}
}

// TestActivateStampsJoinedAtOnlyOnce is what keeps "reinstated last Tuesday"
// from overwriting "joined three years ago".
func TestActivateStampsJoinedAtOnlyOnce(t *testing.T) {
	joined := time.Date(2020, time.March, 1, 12, 0, 0, 0, time.UTC)

	m := &ent.Membership{Status: ent.MembershipSuspended}
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

func TestRoleValidateRejectsAMalformedKey(t *testing.T) {
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
		strings.Repeat("a", ent.MaxRoleKeyLength+1): false,
	}

	for key, want := range tests {
		t.Run(key, func(t *testing.T) {
			role := &ent.Role{Key: key, Name: "Role"}

			err := role.Validate()
			if got := err == nil; got != want {
				t.Errorf("Validate() with key %q = %v, want accepted = %v", key, err, want)
			}
		})
	}
}

// TestSystemRolesRefuseDeletion is the model-level half of the protection. The
// service checks it too, but a hook runs for every delete, including one written
// as a bare delete that never passed through the service.
func TestSystemRolesRefuseDeletion(t *testing.T) {
	role := &ent.Role{Key: "admin", Name: "Administrator", IsSystem: true}
	role.ID = uuid.Must(uuid.NewV7())

	if err := role.RefuseDelete(); !errors.Is(err, ent.ErrRoleIsSystem) {
		t.Errorf("RefuseDelete() on a system role = %v, want ErrRoleIsSystem", err)
	}

	role.IsSystem = false
	if err := role.RefuseDelete(); err != nil {
		t.Errorf("RefuseDelete() on a custom role = %v, want nil", err)
	}
}

// TestRoleBatchDeleteIsRefused closes the way around the check above: a delete
// without a primary key would see IsSystem as false and every system role would
// go.
func TestRoleBatchDeleteIsRefused(t *testing.T) {
	role := &ent.Role{Key: "admin", Name: "Administrator", IsSystem: true}

	if err := role.RefuseDelete(); !errors.Is(err, ent.ErrRoleBatchDeleteUnsupported) {
		t.Errorf("RefuseDelete() without a primary key = %v, want ErrRoleBatchDeleteUnsupported", err)
	}
}

func TestUserSystemRoleValidateValidatesTheKey(t *testing.T) {
	r := &ent.UserSystemRole{RoleKey: "Platform Admin"}
	if err := r.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a malformed key")
	}

	r.RoleKey = "platform_admin"
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestAuthzEventValidateRejectsUnknownActions(t *testing.T) {
	e := &ent.AuthzEvent{Action: "role.exploded", ActorID: uuid.Must(uuid.NewV7())}
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for an unknown action")
	}

	e.Action = ent.ActionRolePermissionsChanged
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestAuthzEventRequiresAnActor keeps the audit trail answering the only
// question it exists for. A row saying a role changed but not who changed it is
// worse than no row, because it looks like coverage.
func TestAuthzEventRequiresAnActor(t *testing.T) {
	e := &ent.AuthzEvent{Action: ent.ActionRoleCreated}

	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for an event with no actor")
	}
}

func TestUserRefuseDeleteRequiresAPrimaryKey(t *testing.T) {
	var u ent.User

	if err := u.RefuseDelete(); !errors.Is(err, ent.ErrBatchDeleteUnsupported) {
		t.Errorf("RefuseDelete() without a primary key = %v, want ErrBatchDeleteUnsupported", err)
	}

	u.ID = uuid.Must(uuid.NewV7())
	if err := u.RefuseDelete(); err != nil {
		t.Errorf("RefuseDelete() on an unprotected account = %v, want nil", err)
	}
}
