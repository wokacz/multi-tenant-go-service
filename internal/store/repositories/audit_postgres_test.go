package repositories_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/audit"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/orgs"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
	"github.com/wokacz/multi-tenant-go-service/internal/store/repositories"
)

// These cover the property the in-memory fake cannot have: the audit row and
// the change it describes share one transaction, so neither can exist without
// the other.
//
//	POSTGRES_TEST=1 go test ./internal/store/repositories -v

// actorContext puts somebody on the context, the way requireBearer does.
func actorContext(t *testing.T, actor uuid.UUID) context.Context {
	t.Helper()

	return audit.WithActor(t.Context(), audit.Actor{
		ID:        actor,
		IP:        "203.0.113.7",
		UserAgent: "probe/1.0",
	})
}

// TestAnAuditRowRollsBackWithItsChange is the reason record takes the
// transaction rather than the pool.
//
// The change here is refused mid-transaction: one of the role ids belongs to
// another organization. Both the assignment and the entry describing it must
// disappear together — a log that records a change the database rejected is a
// log that lies.
func TestAnAuditRowRollsBackWithItsChange(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	foreign := newOrganization(t, db)

	mine := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	theirs := newRole(t, db, foreign.ID, "readers", string(authz.PermMembersRead))

	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive)

	ctx := actorContext(t, u.ID)

	before := countEvents(t, db, org.ID)

	err := repo.ReplaceMemberRoles(ctx, org.ID, membership.ID, []uuid.UUID{mine.ID, theirs.ID}, orgs.RefuseLastOwnerLoss(true))
	if !errors.Is(err, orgs.ErrNotFound) {
		t.Fatalf("ReplaceMemberRoles() = %v, want ErrNotFound", err)
	}

	if got := countEvents(t, db, org.ID); got != before {
		t.Errorf("audit rows went from %d to %d on a change that was rolled back", before, got)
	}
}

// TestAnAuditRowLandsWithItsChange is the same property the other way.
func TestAnAuditRowLandsWithItsChange(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive)

	ctx := actorContext(t, u.ID)

	if err := repo.ReplaceMemberRoles(ctx, org.ID, membership.ID, []uuid.UUID{role.ID}, orgs.RefuseLastOwnerLoss(true)); err != nil {
		t.Fatalf("ReplaceMemberRoles() = %v", err)
	}

	events, err := repo.Events(ctx, org.ID, 10, 0)
	if err != nil {
		t.Fatalf("Events() = _, %v", err)
	}

	if len(events) == 0 {
		t.Fatal("the change committed without its audit row")
	}

	newest := events[0]

	if newest.Action != models.ActionMemberRolesChanged {
		t.Errorf("action = %q, want %q", newest.Action, models.ActionMemberRolesChanged)
	}

	// The actor's name and address come from the join, so a reader does not need
	// a second lookup.
	if newest.Actor.ID != u.ID || newest.Actor.Email != u.Email {
		t.Errorf("actor = %+v, want %v / %q", newest.Actor, u.ID, u.Email)
	}

	if newest.Subject == nil || newest.Subject.ID != u.ID {
		t.Errorf("subject = %+v, want the member the change was about", newest.Subject)
	}

	// The transport facts come from the context, and ip is typed inet.
	if newest.IP != "203.0.113.7" {
		t.Errorf("ip = %q, want the address on the context", newest.IP)
	}
}

// TestNothingIsRecordedWithoutAnActor pins the rule that keeps anonymous rows
// out. A background job changing roles writes no entry rather than one that
// answers none of the questions an audit exists for.
func TestNothingIsRecordedWithoutAnActor(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	role := newRole(t, db, org.ID, "readers", string(authz.PermMembersRead))
	membership := newMembership(t, db, org.ID, u.ID, models.MembershipActive)

	// No actor on the context.
	if err := repo.ReplaceMemberRoles(t.Context(), org.ID, membership.ID, []uuid.UUID{role.ID}, orgs.RefuseLastOwnerLoss(true)); err != nil {
		t.Fatalf("ReplaceMemberRoles() = %v", err)
	}

	if got := countEvents(t, db, org.ID); got != 0 {
		t.Errorf("audit rows = %d, want none — there was nobody to attribute them to", got)
	}
}

// TestTheHistorySurvivesWhatItDescribes is why the role key is captured at write
// time and the actor join is a LEFT one: the entry has to still make sense after
// the role is gone.
func TestTheHistorySurvivesWhatItDescribes(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	org := newOrganization(t, db)
	ctx := actorContext(t, u.ID)

	role, err := repo.CreateRole(ctx, org.ID, &models.Role{Key: "temporary", Name: "Temporary"}, nil)
	if err != nil {
		t.Fatalf("CreateRole() = _, %v", err)
	}

	if err := repo.DeleteRole(ctx, org.ID, role.ID, orgs.RefuseRoleInUse()); err != nil {
		t.Fatalf("DeleteRole() = %v", err)
	}

	events, err := repo.Events(ctx, org.ID, 10, 0)
	if err != nil {
		t.Fatalf("Events() = _, %v", err)
	}

	var deletion *audit.Event

	for i := range events {
		if events[i].Action == models.ActionRoleDeleted {
			deletion = &events[i]

			break
		}
	}

	if deletion == nil {
		t.Fatal("the deletion was not recorded")
	}

	// The role row is gone, so the join yields nothing — the key written at the
	// time is what still names it.
	if deletion.Detail != "temporary" {
		t.Errorf("detail = %q, want the role key captured when it was deleted", deletion.Detail)
	}
}

// TestTheHistoryIsScopedToTheOrganization is the tenancy check at the SQL level.
func TestTheHistoryIsScopedToTheOrganization(t *testing.T) {
	db := testDB(t)
	repo := repositories.NewOrgs(db)

	u := newUser(t, repositories.NewUser(db))
	mine := newOrganization(t, db)
	theirs := newOrganization(t, db)

	ctx := actorContext(t, u.ID)

	if _, err := repo.CreateRole(ctx, theirs.ID, &models.Role{Key: "secret", Name: "Secret"}, nil); err != nil {
		t.Fatalf("CreateRole() = _, %v", err)
	}

	events, err := repo.Events(ctx, mine.ID, 100, 0)
	if err != nil {
		t.Fatalf("Events() = _, %v", err)
	}

	for _, event := range events {
		if event.OrganizationID == nil || *event.OrganizationID != mine.ID {
			t.Errorf("the history of %v contains an entry for %v", mine.ID, event.OrganizationID)
		}
	}
}

func countEvents(t *testing.T, db *store.DB, orgID uuid.UUID) int {
	t.Helper()

	var total int64
	if err := db.Model(&models.AuthzEvent{}).
		Where("organization_id = ?", orgID).
		Count(&total).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}

	return int(total)
}
