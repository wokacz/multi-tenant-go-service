package repositories_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The schema tests read the database, not the structs.
//
// Indexes live in internal/store/ent/schema. A test that read Go tags would answer
// "did somebody write the field" rather than "is the index there". These cases
// query pg_indexes, which is what the running database actually has.
//
// These are also the properties nobody notices breaking. A unique index that quietly
// became non-unique, or a partial index that lost its WHERE, produces no error: it
// produces duplicate rows six months later.

// index is one row of pg_indexes, parsed into the parts worth asserting on.
type index struct {
	name      string
	table     string
	unique    bool
	columns   []string
	predicate string
}

func indexesOf(t *testing.T, db *sql.DB, table string) map[string]index {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT i.relname,
		       ix.indisunique,
		       array_to_string(array_agg(a.attname ORDER BY k.ord), ','),
		       COALESCE(pg_get_expr(ix.indpred, ix.indrelid), '')
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class c ON c.oid = ix.indrelid
		JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE c.relname = $1
		GROUP BY i.relname, ix.indisunique, ix.indpred, ix.indrelid`, table)
	if err != nil {
		t.Fatalf("reading indexes of %s: %v", table, err)
	}

	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()

	out := map[string]index{}

	for rows.Next() {
		var (
			idx     index
			columns string
		)

		if err := rows.Scan(&idx.name, &idx.unique, &columns, &idx.predicate); err != nil {
			t.Fatalf("scanning index: %v", err)
		}

		idx.table = table
		idx.columns = strings.Split(columns, ",")
		out[idx.name] = idx
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterating indexes: %v", err)
	}

	return out
}

// TestTheUniqueIndexesAreInTheDatabase is the reflection test's counterpart, asked of
// Postgres.
func TestTheUniqueIndexesAreInTheDatabase(t *testing.T) {
	db := testDB(t).SQL()

	tests := map[string]struct {
		table   string
		index   string
		columns []string
	}{
		"one membership per user and organization": {
			"memberships", "idx_membership_user_org", []string{"user_id", "organization_id"},
		},
		"one invitation per address and organization": {
			"invitations", "idx_invitation_org_email", []string{"organization_id", "email"},
		},
		"one invitation token across the installation": {
			"invitations", "idx_invitations_token_hash", []string{"token_hash"},
		},
		"one role per invitation": {
			"invitation_roles", "idx_invitation_role", []string{"invitation_id", "role_id"},
		},
		"role keys are unique inside an organization": {
			"roles", "idx_role_org_key", []string{"organization_id", "key"},
		},
		"a permission appears once per role": {
			"role_permissions", "idx_role_permission", []string{"role_id", "permission_key"},
		},
		"a role is assigned once per membership": {
			"membership_roles", "idx_membership_role", []string{"membership_id", "role_id"},
		},
		"a system role is granted once per user": {
			"user_system_roles", "idx_user_system_role", []string{"user_id", "role_key"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			idx, ok := indexesOf(t, db, tc.table)[tc.index]
			if !ok {
				t.Fatalf("%s has no index %s", tc.table, tc.index)
			}

			if !idx.unique {
				t.Errorf("%s is not unique", tc.index)
			}

			if !slices.Equal(idx.columns, tc.columns) {
				t.Errorf("%s columns = %v, want %v", tc.index, idx.columns, tc.columns)
			}

			if idx.predicate != "" {
				t.Errorf("%s is partial (%s); these have to cover every row",
					tc.index, idx.predicate)
			}
		})
	}
}

// TestTheNameIndexesArePartial is the M9 property, and the one most worth reading
// from the database.
//
// A soft-deleted account has to release its address and a soft-deleted organization
// its slug, which is what makes seeding after a reset work and what stops a deletion
// holding a name forever. That behaviour is entirely in the index's WHERE clause: an
// index that lost it still looks like a unique index, still passes a reflection test
// over whatever built it, and refuses the next registration at that address.
func TestTheNameIndexesArePartial(t *testing.T) {
	db := testDB(t).SQL()

	tests := map[string]struct {
		table   string
		index   string
		columns []string
	}{
		"an account's address is unique among live rows": {
			"users", "idx_users_email", []string{"email"},
		},
		"an organization's slug is unique among live rows": {
			"organizations", "idx_organizations_slug", []string{"slug"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			idx, ok := indexesOf(t, db, tc.table)[tc.index]
			if !ok {
				t.Fatalf("%s has no index %s", tc.table, tc.index)
			}

			if !idx.unique {
				t.Errorf("%s is not unique", tc.index)
			}

			if !slices.Equal(idx.columns, tc.columns) {
				t.Errorf("%s columns = %v, want %v", tc.index, idx.columns, tc.columns)
			}

			// The predicate is compared loosely: Postgres normalises it to
			// "(deleted_at IS NULL)" and a future version may spell it differently.
			// What must hold is that deleted_at is what limits the index.
			if !strings.Contains(idx.predicate, "deleted_at IS NULL") {
				t.Errorf("%s predicate = %q, want it limited to deleted_at IS NULL — "+
					"without it a deletion holds the name forever", tc.index, idx.predicate)
			}
		})
	}
}

// TestForeignKeysCascade covers the half of deletion the application does not do
// itself.
//
// Hard-deleting a membership has to take its role assignments with it, and deleting
// an account has to take its devices and login history. The application relies on
// that — RemoveMember deletes one row and expects the cascade to handle
// membership_roles — so a constraint created without ON DELETE CASCADE leaves rows
// pointing at nothing and no error anywhere.
func TestForeignKeysCascade(t *testing.T) {
	db := testDB(t).SQL()

	want := map[string]string{
		"membership_roles.membership_id": "CASCADE",
		"membership_roles.role_id":       "CASCADE",
		"role_permissions.role_id":       "CASCADE",
		"invitation_roles.invitation_id": "CASCADE",
		"invitations.organization_id":    "CASCADE",
		"memberships.organization_id":    "CASCADE",
		"memberships.user_id":            "CASCADE",
		"roles.organization_id":          "CASCADE",
		"devices.user_id":                "CASCADE",
		"login_events.user_id":           "CASCADE",
		"user_system_roles.user_id":      "CASCADE",
	}

	rows, err := db.QueryContext(context.Background(), `
		SELECT c.relname, a.attname, rc.delete_rule
		FROM information_schema.referential_constraints rc
		JOIN pg_constraint con ON con.conname = rc.constraint_name
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum`)
	if err != nil {
		t.Fatalf("reading foreign keys: %v", err)
	}

	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()

	got := map[string]string{}

	for rows.Next() {
		var table, column, rule string
		if err := rows.Scan(&table, &column, &rule); err != nil {
			t.Fatalf("scanning foreign key: %v", err)
		}

		got[fmt.Sprintf("%s.%s", table, column)] = rule
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterating foreign keys: %v", err)
	}

	for key, rule := range want {
		actual, ok := got[key]
		if !ok {
			t.Errorf("%s has no foreign key at all", key)

			continue
		}

		if actual != rule {
			t.Errorf("%s deletes with %q, want %q", key, actual, rule)
		}
	}
}

// TestTheStatusCheckConstraintIsThere pins the one enum the database enforces.
//
// models.MembershipStatus lost "invited" when invitations moved to their own table,
// and the constraint is what stops a caller that is not this application writing it
// back.
func TestTheStatusCheckConstraintIsThere(t *testing.T) {
	db := testDB(t).SQL()

	var definition string

	err := db.QueryRowContext(context.Background(), `
		SELECT pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		WHERE c.relname = 'memberships' AND con.contype = 'c'`).Scan(&definition)
	if err != nil {
		t.Fatalf("reading the check constraint on memberships: %v", err)
	}

	for _, status := range []string{"active", "suspended"} {
		if !strings.Contains(definition, status) {
			t.Errorf("the constraint %q does not allow %q", definition, status)
		}
	}

	if strings.Contains(definition, "invited") {
		t.Errorf("the constraint still allows 'invited': %s", definition)
	}
}

// TestEveryTableHasItsTimestamps is a shape check rather than a behaviour one, and it
// is here because the next schema is generated by a different tool.
//
// Every model embeds models.Model, so every table has id, created_at and updated_at,
// and the soft-deletable ones have deleted_at. A mixin wired to some schemas and not
// others produces exactly this kind of gap, and nothing at runtime complains until
// something reads a column that is not there.
func TestEveryTableHasItsTimestamps(t *testing.T) {
	db := testDB(t).SQL()

	softDeletable := []string{"users", "organizations"}

	rows, err := db.QueryContext(context.Background(), `
		SELECT table_name, string_agg(column_name, ',' ORDER BY column_name)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name <> 'atlas_schema_revisions'
		GROUP BY table_name`)
	if err != nil {
		t.Fatalf("reading columns: %v", err)
	}

	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()

	tables := 0

	for rows.Next() {
		var table, columns string
		if err := rows.Scan(&table, &columns); err != nil {
			t.Fatalf("scanning columns: %v", err)
		}

		tables++

		present := strings.Split(columns, ",")

		for _, want := range []string{"id", "created_at", "updated_at"} {
			if !slices.Contains(present, want) {
				t.Errorf("%s has no %s column", table, want)
			}
		}

		hasDeletedAt := slices.Contains(present, "deleted_at")
		if slices.Contains(softDeletable, table) != hasDeletedAt {
			t.Errorf("%s: deleted_at present = %v, want %v",
				table, hasDeletedAt, slices.Contains(softDeletable, table))
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns: %v", err)
	}

	// A query that matched nothing would pass every assertion above.
	if tables < 10 {
		t.Errorf("only %d tables found; the query is not seeing the schema", tables)
	}
}
