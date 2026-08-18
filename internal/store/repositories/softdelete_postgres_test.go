package repositories_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/schema"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
)

// The interceptor and the delete hook, asked of the client the repositories use.
//
// Every read depends on "a retired row is invisible" holding without being asked.
// The one place a filter like that does not apply — a condition hanging off a
// LEFT JOIN, or an EXISTS from an edge — is where deleted accounts stayed visible
// for months.

func entClient(t *testing.T) *ent.Client {
	t.Helper()

	return testDB(t).Ent()
}

// newEntUser creates an account through ent, since these cases are about ent's view of
// the table rather than the repository's.
func newEntUser(t *testing.T, client *ent.Client) *ent.User {
	t.Helper()

	u, err := client.User.Create().
		SetName("Ada").
		SetEmail("ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com").
		SetPasswordHash("not-a-real-hash").
		Save(context.Background())
	if err != nil {
		t.Fatalf("creating a user through ent: %v", err)
	}

	return u
}

// TestDeletingThroughEntRetiresTheRow is the hook: a delete becomes an update, so
// nothing has to remember to write a timestamp instead of issuing a DELETE.
func TestDeletingThroughEntRetiresTheRow(t *testing.T) {
	client := entClient(t)
	ctx := context.Background()

	u := newEntUser(t, client)

	if err := client.User.DeleteOne(u).Exec(ctx); err != nil {
		t.Fatalf("DeleteOne() = %v", err)
	}

	// Gone from an ordinary read.
	if _, err := client.User.Get(ctx, u.ID); !ent.IsNotFound(err) {
		t.Errorf("Get() after deleting = %v, want not-found", err)
	}

	// Still there, with a timestamp, for a caller that says so.
	retired, err := client.User.Get(schema.SkipSoftDelete(ctx), u.ID)
	if err != nil {
		t.Fatalf("Get(SkipSoftDelete) = %v; the row was hard deleted", err)
	}

	if retired.DeletedAt == nil {
		t.Error("the row survived but carries no deleted_at")
	}
}

// TestAnOrdinaryQueryCannotSeeARetiredRow is the interceptor, asked through a query
// rather than a lookup by id — the shape a listing uses.
func TestAnOrdinaryQueryCannotSeeARetiredRow(t *testing.T) {
	client := entClient(t)
	ctx := context.Background()

	u := newEntUser(t, client)

	if err := client.User.DeleteOne(u).Exec(ctx); err != nil {
		t.Fatalf("DeleteOne() = %v", err)
	}

	found, err := client.User.Query().Where(entuser.Email(u.Email)).All(ctx)
	if err != nil {
		t.Fatalf("Query() = %v", err)
	}

	if len(found) != 0 {
		t.Errorf("a query found %d retired rows", len(found))
	}

	found, err = client.User.Query().Where(entuser.Email(u.Email)).All(schema.SkipSoftDelete(ctx))
	if err != nil {
		t.Fatalf("Query(SkipSoftDelete) = %v", err)
	}

	if len(found) != 1 {
		t.Errorf("SkipSoftDelete found %d rows, want the retired one", len(found))
	}
}

// TestSkipSoftDeleteReallyDeletes is the other half of the escape hatch, and the one the
// seeder's reset needs: retiring a row twice would move its timestamp, so a reset has to
// be able to remove what it retired.
func TestSkipSoftDeleteReallyDeletes(t *testing.T) {
	client := entClient(t)
	ctx := context.Background()

	u := newEntUser(t, client)

	if err := client.User.DeleteOne(u).Exec(schema.SkipSoftDelete(ctx)); err != nil {
		t.Fatalf("DeleteOne(SkipSoftDelete) = %v", err)
	}

	if _, err := client.User.Get(schema.SkipSoftDelete(ctx), u.ID); !ent.IsNotFound(err) {
		t.Errorf("the row is still there after a hard delete: %v", err)
	}
}

// TestRetiringDoesNotRewriteWhenItHappened pins the predicate on the hook. Without it a
// second delete would update the timestamp of a row already retired, and "when was this
// account deleted" would answer with the last time somebody tried.
func TestRetiringDoesNotRewriteWhenItHappened(t *testing.T) {
	client := entClient(t)
	ctx := context.Background()

	u := newEntUser(t, client)

	if err := client.User.DeleteOne(u).Exec(ctx); err != nil {
		t.Fatalf("first delete = %v", err)
	}

	first, err := client.User.Get(schema.SkipSoftDelete(ctx), u.ID)
	if err != nil {
		t.Fatalf("Get(SkipSoftDelete) = %v", err)
	}

	// The second attempt finds nothing to retire, which ent reports as not-found.
	err = client.User.DeleteOne(u).Exec(ctx)
	if err != nil && !ent.IsNotFound(err) {
		t.Fatalf("second delete = %v, want nil or not-found", err)
	}

	second, err := client.User.Get(schema.SkipSoftDelete(ctx), u.ID)
	if err != nil {
		t.Fatalf("Get(SkipSoftDelete) = %v", err)
	}

	if !second.DeletedAt.Equal(*first.DeletedAt) {
		t.Errorf("deleted_at moved from %s to %s", first.DeletedAt, second.DeletedAt)
	}
}

// TestTheEntClientSharesTheGormPool is the property the whole migration rests on: one
// pool, two clients, so a repository can move without the connection count doubling and
// without the two halves of a change landing in different transactions.
func TestTheEntClientSharesTheGormPool(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var before int

	row := db.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`)
	if err := row.Scan(&before); err != nil {
		t.Fatalf("counting connections: %v", err)
	}

	// Enough queries through ent to have opened a pool of its own, if it had one.
	for range 8 {
		if _, err := db.Ent().User.Query().Limit(1).All(ctx); err != nil {
			t.Fatalf("ent query: %v", err)
		}
	}

	var after int

	row = db.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`)
	if err := row.Scan(&after); err != nil {
		t.Fatalf("counting connections: %v", err)
	}

	// The pool is capped at four in the test configuration, so a second pool would be
	// visible as more connections than that.
	if after > before+4 {
		t.Errorf("connections went from %d to %d; ent appears to have a pool of its own",
			before, after)
	}
}
