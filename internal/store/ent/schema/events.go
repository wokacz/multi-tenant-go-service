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

// LoginEvent is one attempt to sign in, whatever came of it.
type LoginEvent struct {
	ent.Schema
}

func (LoginEvent) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (LoginEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}),

		// Optional: an attempt can fail before any device is resolved.
		//
		// A plain column with no edge, which is what the table has today — and what it
		// should have. An edge would add ON DELETE CASCADE, and then revoking a device
		// would delete the sign-in history that mentions it. The history is the thing
		// somebody reads *after* a device is gone.
		field.UUID("device_id", uuid.UUID{}).Optional().Nillable(),

		field.String("ip").SchemaType(inetType),
		field.String("user_agent").MaxLen(512).SchemaType(varchar(512)).Optional(),

		// Unlike the membership status, this enum has a CHECK because the values are
		// written by exactly one place and read by a screen: a fifth outcome nobody
		// declared would render as a blank.
		field.Enum("outcome").
			Values("success", "bad_password", "mfa_failed", "locked").
			SchemaType(varchar20),

		field.String("country").MaxLen(2).SchemaType(varchar(2)).Optional(),
	}
}

func (LoginEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("login_events").Field("user_id").Unique().Required(),
	}
}

func (LoginEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at").StorageKey("idx_login_user_time"),
		index.Fields("device_id", "created_at").StorageKey("idx_login_device_time"),
		index.Fields("ip").StorageKey("idx_login_events_ip"),
	}
}

func (LoginEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "login_events",
			Checks: map[string]string{
				"chk_login_events_outcome": "(outcome)::text = ANY ((ARRAY['success'::character varying, 'bad_password'::character varying, 'mfa_failed'::character varying, 'locked'::character varying])::text[])",
			},
		},
	}
}

// AuthzEvent is one change to who may do what.
//
// It has no edges at all, and that is the design rather than an omission: the table is
// append-only and outlives what it describes. A membership deleted last year still has
// its "member.removed" entry, and a foreign key would either have deleted the entry
// with the row or refused the deletion — which is why every id here is a plain column.
type AuthzEvent struct {
	ent.Schema
}

func (AuthzEvent) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (AuthzEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("organization_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("actor_id", uuid.UUID{}),
		field.UUID("subject_id", uuid.UUID{}).Optional().Nillable(),
		field.String("action").MaxLen(40).SchemaType(varchar(40)).NotEmpty(),
		field.UUID("role_id", uuid.UUID{}).Optional().Nillable(),
		field.String("permission_key").MaxLen(100).SchemaType(varchar(100)).Optional(),
		field.String("ip").SchemaType(inetType),
		field.String("user_agent").MaxLen(512).SchemaType(varchar(512)).Optional(),
		field.String("detail").MaxLen(500).SchemaType(varchar(500)).Optional(),
	}
}

func (AuthzEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id", "created_at").StorageKey("idx_authz_org_time"),
		index.Fields("actor_id", "created_at").StorageKey("idx_authz_actor_time"),
		index.Fields("subject_id").StorageKey("idx_authz_events_subject_id"),
	}
}

func (AuthzEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "authz_events"}}
}
