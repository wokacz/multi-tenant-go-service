package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Device is one client a sign-in was attributed to.
type Device struct {
	ent.Schema
}

func (Device) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (Device) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		field.String("fingerprint").MaxLen(64).NotEmpty().Sensitive(),
		field.String("label").MaxLen(100).Optional(),
		field.String("user_agent").MaxLen(512).Optional(),
		field.Time("last_seen_at").Optional().Nillable(),
		// Nillable as well as Optional: models.Device carries *string, and "never seen"
		// has to stay distinguishable from "seen from an empty address".
		field.String("last_ip").Optional().Nillable().SchemaType(inetType),

		// Two timestamps rather than two booleans: "when was this trusted" and "when
		// was it revoked" are questions somebody asks, and a boolean cannot answer
		// either.
		field.Time("trusted_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (Device) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("devices").Field("user_id").Unique().Required(),
		edge.To("challenges", TwoFactorChallenge.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Device) Indexes() []ent.Index {
	return []ent.Index{
		// One row per fingerprint per account. Two accounts on the same browser
		// legitimately share a fingerprint, which is why the account is in the key.
		index.Fields("user_id", "fingerprint").Unique().StorageKey("idx_device_user_fp"),
		index.Fields("last_seen_at").StorageKey("idx_devices_last_seen_at"),
	}
}
