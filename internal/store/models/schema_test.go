package models_test

import (
	"slices"
	"sync"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// GORM builds a composite index only when several fields carry the same index
// name. A lone `index:name,priority:1` tag silently degrades to a single-column
// index, which is easy to write and impossible to notice — hence these tests.
func indexOf(t *testing.T, model any, name string) *schema.Index {
	t.Helper()

	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse(%T) = %v, want nil", model, err)
	}

	for _, idx := range s.ParseIndexes() {
		if idx.Name == name {
			return idx
		}
	}

	t.Fatalf("index %q not found on %T", name, model)

	return nil
}

func indexColumns(idx *schema.Index) []string {
	cols := make([]string, 0, len(idx.Fields))
	for _, f := range idx.Fields {
		cols = append(cols, f.DBName)
	}

	return cols
}

func TestDeviceFingerprintIsUniquePerUser(t *testing.T) {
	idx := indexOf(t, &models.Device{}, "idx_device_user_fp")

	want := []string{"user_id", "fingerprint"}
	if got := indexColumns(idx); !slices.Equal(got, want) {
		t.Errorf("idx_device_user_fp columns = %v, want %v "+
			"(a fingerprint-only unique index would bar two users from sharing a device)", got, want)
	}
	if idx.Class != "UNIQUE" {
		t.Errorf("idx_device_user_fp class = %q, want %q", idx.Class, "UNIQUE")
	}
}

func TestLoginEventTimeIndexes(t *testing.T) {
	tests := map[string][]string{
		"idx_login_user_time":   {"user_id", "created_at"},
		"idx_login_device_time": {"device_id", "created_at"},
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := indexColumns(indexOf(t, &models.LoginEvent{}, name)); !slices.Equal(got, want) {
				t.Errorf("%s columns = %v, want %v", name, got, want)
			}
		})
	}
}

// TestAuthzEventTimeIndexes covers the same shadowed-CreatedAt trap on the audit
// table. Without the shadow these degrade to indexes on the id column alone, and
// "show me this organization's recent changes" becomes a sequential scan over
// every change any organization ever made.
func TestAuthzEventTimeIndexes(t *testing.T) {
	tests := map[string][]string{
		"idx_authz_org_time":   {"organization_id", "created_at"},
		"idx_authz_actor_time": {"actor_id", "created_at"},
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := indexColumns(indexOf(t, &models.AuthzEvent{}, name)); !slices.Equal(got, want) {
				t.Errorf("%s columns = %v, want %v", name, got, want)
			}
		})
	}
}

// TestAuthorizationUniqueIndexes pins the constraints the authorization rules
// lean on. Each of these is what makes a duplicate impossible rather than merely
// unlikely: two memberships in one organization would give a user two permission
// sets, and a repeated role assignment would make revoking a role leave one
// copy behind.
func TestAuthorizationUniqueIndexes(t *testing.T) {
	tests := map[string]struct {
		model any
		index string
		want  []string
	}{
		"one membership per user and organization": {
			&models.Membership{}, "idx_membership_user_org", []string{"user_id", "organization_id"},
		},
		// The address is no longer on a membership: an invitation is not one, and
		// carried the address only because it had no account to point at.
		"one invitation per address and organization": {
			&models.Invitation{}, "idx_invitation_org_email", []string{"organization_id", "email"},
		},
		"one invitation token is unique across the installation": {
			&models.Invitation{}, "idx_invitations_token_hash", []string{"token_hash"},
		},
		"one role per invitation": {
			&models.InvitationRole{}, "idx_invitation_role", []string{"invitation_id", "role_id"},
		},
		"role keys are unique inside an organization": {
			&models.Role{}, "idx_role_org_key", []string{"organization_id", "key"},
		},
		"a permission appears once per role": {
			&models.RolePermission{}, "idx_role_permission", []string{"role_id", "permission_key"},
		},
		"a role is assigned once per membership": {
			&models.MembershipRole{}, "idx_membership_role", []string{"membership_id", "role_id"},
		},
		"a system role is granted once per user": {
			&models.UserSystemRole{}, "idx_user_system_role", []string{"user_id", "role_key"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			idx := indexOf(t, tc.model, tc.index)

			if got := indexColumns(idx); !slices.Equal(got, tc.want) {
				t.Errorf("%s columns = %v, want %v", tc.index, got, tc.want)
			}

			if idx.Class != "UNIQUE" {
				t.Errorf("%s class = %q, want %q", tc.index, idx.Class, "UNIQUE")
			}
		})
	}
}
