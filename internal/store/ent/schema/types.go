package schema

import "entgo.io/ent/dialect"

// inetType is the column type for an address.
//
// inet rather than text, so Postgres validates what goes in and comparisons mean what
// they look like. It is the one column type ent does not derive on its own, and the
// reason the store casts explicitly on write.
//
// The string widths that used to live here are gone. MaxLen is ent's validator and
// renders an unbounded varchar in Postgres, which is ent's convention and now the
// schema's: the width is enforced in Go, where every other length rule in this
// codebase already lives, rather than twice.
var inetType = map[string]string{dialect.Postgres: "inet"}
