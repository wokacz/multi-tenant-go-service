package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Membership is one account's place in one organization.
type Membership struct {
	ent.Schema
}

func (Membership) Mixin() []ent.Mixin {
	return []ent.Mixin{Model{}}
}

func (Membership) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		field.UUID("organization_id", uuid.UUID{}),

		// Two values, and the CHECK is what keeps a third out. "invited" was the
		// third until invitations moved to a table of their own, and the constraint
		// is what stops anything that is not this application writing it back.
		// varchar(20) rather than ent's default text, because that is the column the
		// database has. ent has no MaxLen on an enum, so the type is stated directly.
		field.Enum("status").
			Values("active", "suspended").
			SchemaType(varchar20),

		field.UUID("invited_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("joined_at").Optional().Nillable(),
	}
}

func (Membership) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("memberships").Field("user_id").Unique().Required(),
		edge.From("organization", Organization.Type).Ref("memberships").
			Field("organization_id").Unique().Required(),

		edge.To("roles", MembershipRole.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Membership) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "organization_id").
			Unique().
			StorageKey("idx_membership_user_org"),
	}
}

func (Membership) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "memberships",
			// Named, because the existing constraint is named — an unnamed one would
			// arrive with a generated name and read as a change where there is none.
			Checks: map[string]string{
				"chk_memberships_status": "(status)::text = ANY ((ARRAY['active'::character varying, 'suspended'::character varying])::text[])",
			},
		},
	}
}
