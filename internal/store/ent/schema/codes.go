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

// The three emailed codes share a shape: a hash, an expiry, an attempt counter and a
// consumed marker. They are separate tables rather than one with a "purpose" column
// because the HMAC that hashes them is purpose-separated — a reset code must not be
// usable as a two-factor code — and one table would invite exactly that mistake.

// codeFields is the shape all three share.
func codeFields() []ent.Field {
	return []ent.Field{
		field.String("code_hash").MaxLen(64).SchemaType(varchar(64)).NotEmpty().Sensitive(),
		field.Time("expires_at"),

		// The counter moves in a single conditional UPDATE, never read-modify-write:
		// concurrent guesses would otherwise all read the same value and write the
		// same value, and a five-attempt cap would stop capping.
		//
		// No Default: the column has none today, and the rows are always created with
		// the counter written explicitly. A default here would be a column change
		// dressed up as a port.
		field.Int("attempts"),

		field.Time("consumed_at").Optional().Nillable(),
	}
}

// PasswordReset is a code emailed to an address that asked to reset its password.
type PasswordReset struct {
	ent.Schema
}

func (PasswordReset) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (PasswordReset) Fields() []ent.Field {
	return append([]ent.Field{field.UUID("user_id", uuid.UUID{})}, codeFields()...)
}

func (PasswordReset) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("password_resets").Field("user_id").Unique().Required(),
	}
}

func (PasswordReset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").StorageKey("idx_password_resets_user_id"),
		index.Fields("expires_at").StorageKey("idx_password_resets_expires_at"),
	}
}

func (PasswordReset) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "password_resets"}}
}

// EmailChange is a code emailed to the address an account wants to move to.
type EmailChange struct {
	ent.Schema
}

func (EmailChange) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (EmailChange) Fields() []ent.Field {
	return append([]ent.Field{
		field.UUID("user_id", uuid.UUID{}),
		field.String("new_email").MaxLen(255).SchemaType(varchar(255)).NotEmpty(),
	}, codeFields()...)
}

func (EmailChange) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("email_changes").Field("user_id").Unique().Required(),
	}
}

func (EmailChange) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").StorageKey("idx_email_changes_user_id"),
		index.Fields("expires_at").StorageKey("idx_email_changes_expires_at"),
	}
}

func (EmailChange) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "email_changes"}}
}

// TwoFactorChallenge is a code emailed during a sign-in from an untrusted device.
type TwoFactorChallenge struct {
	ent.Schema
}

func (TwoFactorChallenge) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (TwoFactorChallenge) Fields() []ent.Field {
	return append([]ent.Field{
		field.UUID("user_id", uuid.UUID{}),

		// Required, unlike on a login event: a challenge is raised *for* a device, and
		// one raised for a different device must not verify.
		field.UUID("device_id", uuid.UUID{}),
	}, codeFields()...)
}

func (TwoFactorChallenge) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("challenges").Field("user_id").Unique().Required(),
		edge.From("device", Device.Type).Ref("challenges").Field("device_id").Unique().Required(),
	}
}

func (TwoFactorChallenge) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").StorageKey("idx_two_factor_challenges_user_id"),
		index.Fields("device_id").StorageKey("idx_two_factor_challenges_device_id"),
		index.Fields("expires_at").StorageKey("idx_two_factor_challenges_expires_at"),
	}
}

func (TwoFactorChallenge) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "two_factor_challenges"}}
}
