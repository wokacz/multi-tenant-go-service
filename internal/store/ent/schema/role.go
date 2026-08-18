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

// Role is a named set of permissions inside one organization.
type Role struct {
	ent.Schema
}

func (Role) Mixin() []ent.Mixin {
	return []ent.Mixin{Model{}}
}

func (Role) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("organization_id", uuid.UUID{}),
		field.String("key").MaxLen(64).SchemaType(varchar(64)).NotEmpty(),
		field.String("name").MaxLen(100).SchemaType(varchar(100)).NotEmpty(),
		field.String("description").MaxLen(255).SchemaType(varchar(255)).Optional(),

		// Marks a role materialised from the shipped catalog when the organization
		// was created. The API renders those from the message catalog rather than
		// from the name column, which is why the flag has to survive.
		field.Bool("is_system").Default(false),
	}
}

func (Role) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("organization", Organization.Type).
			Ref("roles").
			Field("organization_id").
			Unique().
			Required(),

		edge.To("permissions", RolePermission.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("membership_roles", MembershipRole.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("invitation_roles", InvitationRole.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Role) Indexes() []ent.Index {
	return []ent.Index{
		// Keys repeat across tenants by design — every organization gets its own
		// "owner" — so the uniqueness is per organization and never global.
		index.Fields("organization_id", "key").
			Unique().
			StorageKey("idx_role_org_key"),
	}
}

func (Role) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "roles"}}
}
