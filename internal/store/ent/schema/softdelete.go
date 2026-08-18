package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

// SoftDelete carries the two columns a retired row keeps: when it was retired, and
// whether it may be retired at all.
//
// **Only the columns.** The behaviour — filtering deleted rows out of every read,
// turning a delete into an update, refusing a protected row — is stage 2, and it is
// not here yet for a reason that is worth stating rather than discovering: the
// interceptor and hook that implement it have to import the *generated* packages
// (intercept, gen), which do not exist until the client has been generated once. So
// the first generation cannot contain them.
//
// Until stage 2 wires it, an ent query on these tables sees deleted rows. Nothing in
// the application uses ent yet, so that is inert — but a reader who assumed otherwise
// would assume wrongly.
type SoftDelete struct {
	mixin.Schema
}

func (SoftDelete) Fields() []ent.Field {
	return []ent.Field{
		field.Time("deleted_at").
			Optional().
			Nillable(),

		// The default organization carries this, and it is what makes
		// models.ErrProtected reachable: an installation that lost its only
		// organization has no working accounts and no screen to undo it from.
		// Nullable and without a default, matching what GORM produced from an
		// untagged bool. Same reasoning as the timestamps above.
		field.Bool("is_protected").
			Optional(),
	}
}
