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

// File is the metadata of one uploaded blob. The bytes themselves live outside
// the database — AES-256-GCM ciphertext under the process key — so this table
// can be listed, authorised and deleted without ever reading the payload.
type File struct {
	ent.Schema
}

func (File) Mixin() []ent.Mixin { return []ent.Mixin{Model{}} }

func (File) Fields() []ent.Field {
	return []ent.Field{
		// Nullable: an account photo (and later, anything that is not a tenant
		// document) has no organization. Org-scoped queries filter by this
		// column, so a NULL row cannot leak into GET /v1/orgs/{id}/files.
		field.UUID("organization_id", uuid.UUID{}).Optional().Nillable(),

		// A plain column, not an edge. The file outlives the uploader's
		// membership: a foreign key with CASCADE would delete the evidence when
		// the account is hard-deleted, and SET NULL would lose the only record
		// of who put it there. Soft-deleted accounts keep their id, so the
		// join the audit log already does still resolves a name.
		field.UUID("uploaded_by", uuid.UUID{}),

		field.String("original_name").MaxLen(255).NotEmpty(),
		field.String("declared_type").MaxLen(127).Optional(),
		field.String("detected_type").MaxLen(127).NotEmpty(),
		field.Int64("size_bytes"),
		field.String("sha256").MaxLen(64).NotEmpty(),
		field.String("storage_key").MaxLen(512).NotEmpty(),
		field.String("encryption_key_id").MaxLen(64).NotEmpty(),

		// Infected payloads are never stored, so there is no "infected" value.
		// unavailable is the optional-scan path: the scanner could not be
		// reached and the operator chose to accept the file anyway.
		field.Enum("scan_status").
			Values("skipped", "clean", "unavailable"),
		field.String("scan_engine").MaxLen(64).Optional(),
	}
}

func (File) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("organization", Organization.Type).
			Ref("files").
			Field("organization_id").
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("avatar_of", User.Type).
			Unique(),
	}
}

func (File) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("organization_id", "id").
			StorageKey("idx_files_org_id"),
		index.Fields("organization_id", "created_at").
			StorageKey("idx_files_org_created"),
	}
}

func (File) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "files",
			Checks: map[string]string{
				"chk_files_scan_status": "(scan_status)::text = ANY ((ARRAY['skipped'::character varying, 'clean'::character varying, 'unavailable'::character varying])::text[])",
			},
		},
	}
}
