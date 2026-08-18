package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Invitation is an outstanding offer of membership.
//
// Its identity is the hash of a secret, not the address — that is what closed the
// account pre-hijacking problem, and why token_hash is unique across the whole
// installation rather than per organization.
type Invitation struct {
	ent.Schema
}

func (Invitation) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (Invitation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("organization_id", uuid.UUID{}),

		// Kept even though the token identifies the invitation: accepting compares it
		// against the account's address, which is the narrower rule chosen in D4.
		field.String("email").MaxLen(255).NotEmpty(),

		field.String("token_hash").MaxLen(64).NotEmpty().Sensitive(),
		field.UUID("invited_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("expires_at"),
		field.Time("accepted_at").Optional().Nillable(),
	}
}

func (Invitation) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("organization", Organization.Type).Ref("invitations").
			Field("organization_id").Unique().Required(),

		edge.To("roles", InvitationRole.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Invitation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id", "email").Unique().StorageKey("idx_invitation_org_email"),
		index.Fields("token_hash").Unique().StorageKey("idx_invitations_token_hash"),
		index.Fields("expires_at").StorageKey("idx_invitations_expires_at"),
	}
}
