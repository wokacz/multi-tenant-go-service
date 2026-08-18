package schema

import (
	"fmt"

	"entgo.io/ent/dialect"
)

// The column types ent does not derive on its own.
//
// field.String().MaxLen(n) is a *validator*: it refuses an over-long value in Go and
// leaves the column as an unbounded varchar. That was read out of ent's Postgres
// dialect rather than assumed — it maps every string to varchar and only reaches for
// text above ten megabytes.
//
// The lengths are kept rather than dropped, and that is a decision. Postgres stores
// varchar(n) and text identically, so this is not about space: it is the backstop that
// refuses an over-long value written by something that is not this application, and
// the audit path already relies on a column limit when it truncates a user agent.
// Dropping them would be a migration altering twenty-five columns, not a tidy-up.

// varchar is the sized character type, stated per field because ent will not.
func varchar(n int) map[string]string {
	return map[string]string{dialect.Postgres: fmt.Sprintf("character varying(%d)", n)}
}

// inetType is the column type for an address.
//
// inet rather than text, so Postgres validates what goes in and comparisons mean what
// they look like. It is the reason the store casts explicitly on write.
var inetType = map[string]string{dialect.Postgres: "inet"}

// varchar20 is the width the two enum columns have. ent renders an enum as varchar
// without a length, for backwards compatibility with its own older versions.
var varchar20 = varchar(20)
