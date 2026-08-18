package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User is the first schema, and it is here to prove the pipeline rather than to
// finish the model: the remaining twelve and every edge between them are stage 2.
//
// Column for column against models.User, because the acceptance test for stage 2 is
// that Atlas finds nothing to change against the migration already in migrations/.
type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{Model{}, SoftDelete{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(100).
			NotEmpty(),

		field.String("email").
			MaxLen(255).
			NotEmpty(),

		field.String("password_hash").
			MaxLen(255).
			NotEmpty().
			Sensitive(),

		// Nullable, and that is the whole point: an account that never chose a
		// language negotiates one per request, and a default here would turn a
		// guess into a permanent decision.
		field.String("locale").
			MaxLen(10).
			Optional(),

		field.Int("session_epoch").
			Default(0),

		// Missed on the first pass, because that pass was written from memory of the
		// model rather than from the model. The comparison against the migration is
		// what caught it, which is the argument for stage 2's acceptance test being a
		// diff rather than a reading.
		field.Bool("two_factor_enabled").
			Default(false),

		field.Time("suspended_at").
			Optional().
			Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("memberships", Membership.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("devices", Device.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("login_events", LoginEvent.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("password_resets", PasswordReset.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("email_changes", EmailChange.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("challenges", TwoFactorChallenge.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("system_roles", UserSystemRole.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		// Partial, and this is the M9 property: unique among live rows only, so a
		// soft-deleted account releases its address instead of holding it forever.
		// Without the predicate the index still looks unique and still passes a
		// reflection test — it just refuses the next registration at that address.
		index.Fields("email").
			Unique().
			Annotations(entsql.IndexWhere("deleted_at IS NULL")).
			StorageKey("idx_users_email"),

		// Every read on this table carries "deleted_at IS NULL"; without this the
		// predicate is a sequential scan.
		index.Fields("deleted_at").
			StorageKey("idx_users_deleted_at"),
	}
}

// Annotations pins the table name. ent would pluralise "User" to "users" anyway, but
// the migration's acceptance test is a byte comparison against an existing schema,
// and a name arrived at by a naming rule is a name that changes when the rule does.
func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "users"},
	}
}
