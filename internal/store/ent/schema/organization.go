package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Organization is a tenant.
type Organization struct {
	ent.Schema
}

func (Organization) Mixin() []ent.Mixin {
	return []ent.Mixin{Model{}, SoftDelete{}}
}

func (Organization) Fields() []ent.Field {
	return []ent.Field{
		field.String("slug").MaxLen(63).NotEmpty().Immutable(),
		field.String("name").MaxLen(100).NotEmpty(),
	}
}

func (Organization) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("roles", Role.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("memberships", Membership.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("invitations", Invitation.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Organization) Indexes() []ent.Index {
	return []ent.Index{
		// Partial, like the account's address: a soft-deleted organization releases
		// its slug. See the note on User.
		index.Fields("slug").
			Unique().
			Annotations(entsql.IndexWhere("deleted_at IS NULL")).
			StorageKey("idx_organizations_slug"),

		// Every read on this table carries "deleted_at IS NULL"; without this the
		// predicate is a sequential scan.
		index.Fields("deleted_at").
			StorageKey("idx_organizations_deleted_at"),
	}
}
