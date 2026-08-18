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

// The join tables are entities rather than ent's implicit many-to-many edges,
// because each carries columns of its own — who granted a role, which permission key
// — and because the repositories query them directly. An implicit join table would
// have neither a name this codebase chose nor a place to put granted_by.

// MembershipRole assigns a role to a membership.
type MembershipRole struct {
	ent.Schema
}

func (MembershipRole) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (MembershipRole) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("membership_id", uuid.UUID{}),
		field.UUID("role_id", uuid.UUID{}),
		field.UUID("granted_by", uuid.UUID{}).Optional().Nillable(),
	}
}

func (MembershipRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("membership", Membership.Type).Ref("roles").
			Field("membership_id").Unique().Required(),
		edge.From("role", Role.Type).Ref("membership_roles").
			Field("role_id").Unique().Required(),
	}
}

func (MembershipRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("membership_id", "role_id").Unique().StorageKey("idx_membership_role"),
	}
}

func (MembershipRole) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "membership_roles"}}
}

// RolePermission is one permission key granted by one role.
//
// The key is a string rather than a foreign key to a table of permissions, and that
// is deliberate: the catalog of permissions is code — it ships with the binary and
// goes through review — so a row naming a key this build does not define grants
// nothing rather than being impossible to write.
type RolePermission struct {
	ent.Schema
}

func (RolePermission) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (RolePermission) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("role_id", uuid.UUID{}),
		field.String("permission_key").MaxLen(100).SchemaType(varchar(100)).NotEmpty(),
	}
}

func (RolePermission) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("role", Role.Type).Ref("permissions").
			Field("role_id").Unique().Required(),
	}
}

func (RolePermission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("role_id", "permission_key").Unique().StorageKey("idx_role_permission"),
	}
}

func (RolePermission) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "role_permissions"}}
}

// InvitationRole is the role set an invitation promises.
type InvitationRole struct {
	ent.Schema
}

func (InvitationRole) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (InvitationRole) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("invitation_id", uuid.UUID{}),
		field.UUID("role_id", uuid.UUID{}),
	}
}

func (InvitationRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("invitation", Invitation.Type).Ref("roles").
			Field("invitation_id").Unique().Required(),
		edge.From("role", Role.Type).Ref("invitation_roles").
			Field("role_id").Unique().Required(),
	}
}

func (InvitationRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("invitation_id", "role_id").Unique().StorageKey("idx_invitation_role"),
	}
}

func (InvitationRole) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "invitation_roles"}}
}

// UserSystemRole is an installation-wide grant, addressed by key for the same reason
// RolePermission is: the installation's roles are code.
type UserSystemRole struct {
	ent.Schema
}

func (UserSystemRole) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (UserSystemRole) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		field.String("role_key").MaxLen(64).SchemaType(varchar(64)).NotEmpty(),
		field.UUID("granted_by", uuid.UUID{}).Optional().Nillable(),
	}
}

func (UserSystemRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("system_roles").
			Field("user_id").Unique().Required(),
	}
}

func (UserSystemRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "role_key").Unique().StorageKey("idx_user_system_role"),
	}
}

func (UserSystemRole) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "user_system_roles"}}
}
