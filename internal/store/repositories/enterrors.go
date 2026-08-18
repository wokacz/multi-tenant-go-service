package repositories

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// Translating ent's errors into the domain's vocabulary.
//
// The domain deals in user.ErrNotFound and orgs.ErrAlreadyMember, and internal/api
// maps those onto status codes without knowing a database was involved. A raw driver
// error reaching that far arrives as an opaque 500.
//
// ent reports a constraint violation as ent.ConstraintError wrapping the driver's
// error, and the driver's error is what says *which* constraint. Postgres codes rather
// than message matching: 23505 and 23503 are stable across versions and locales, where
// "duplicate key value violates unique constraint" is neither.

// Postgres integrity-violation codes. Named, because 23505 in a condition is a number
// somebody has to look up.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// isNotFound reports whether ent found nothing.
//
// A miss is ordinary control flow here — the API turns it into a 404 — which is why
// every repository maps it to its own not-found error rather than passing ent's up.
func isNotFound(err error) bool {
	return ent.IsNotFound(err)
}

// isUniqueViolation reports whether the write collided with a unique index.
//
// Which index is deliberately not distinguished. A caller that needs to tell "this
// address is taken" from "this key is taken" is a caller writing to one table, and it
// knows which of its indexes can collide; a helper that guessed from the constraint
// name would tie the domain's errors to names the schema generator chooses.
func isUniqueViolation(err error) bool {
	return hasPGCode(err, pgUniqueViolation)
}

// isForeignKeyViolation reports whether the write named a row that does not exist.
//
// AddMember uses this to decide whether the account exists: the membership's
// user_id foreign key is the check, and a raw 23503 would otherwise arrive as an
// opaque 500 on a platform endpoint that takes an account id from a request.
func isForeignKeyViolation(err error) bool {
	return hasPGCode(err, pgForeignKeyViolation)
}

func hasPGCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == code
	}

	return false
}
