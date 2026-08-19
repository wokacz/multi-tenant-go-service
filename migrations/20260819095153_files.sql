-- Create "files" table
CREATE TABLE "files" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "uploaded_by" uuid NOT NULL, "original_name" character varying NOT NULL, "declared_type" character varying NULL, "detected_type" character varying NOT NULL, "size_bytes" bigint NOT NULL, "sha256" character varying NOT NULL, "storage_key" character varying NOT NULL, "encryption_key_id" character varying NOT NULL, "scan_status" character varying NOT NULL, "scan_engine" character varying NULL, "organization_id" uuid NULL, PRIMARY KEY ("id"), CONSTRAINT "files_organizations_files" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_files_scan_status" CHECK ((scan_status)::text = ANY (ARRAY[('skipped'::character varying)::text, ('clean'::character varying)::text, ('unavailable'::character varying)::text])));
-- Create index "idx_files_org_created" to table: "files"
CREATE INDEX "idx_files_org_created" ON "files" ("organization_id", "created_at");
-- Create index "idx_files_org_id" to table: "files"
CREATE INDEX "idx_files_org_id" ON "files" ("organization_id", "id");
-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "avatar_id" uuid NULL, ADD CONSTRAINT "users_files_avatar_of" FOREIGN KEY ("avatar_id") REFERENCES "files" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "users_avatar_id_key" to table: "users"
CREATE UNIQUE INDEX "users_avatar_id_key" ON "users" ("avatar_id");
-- Backfill shipped roles. The catalog grew files.read/create/delete; those
-- keys are copied per organization at create time, so existing tenants would
-- otherwise have an admin/member/viewer/owner that cannot use the new routes.
-- ON CONFLICT: a tenant created after this migration already has the rows.
INSERT INTO "role_permissions" ("id", "created_at", "updated_at", "permission_key", "role_id")
SELECT gen_random_uuid(),
       NOW(),
       NOW(),
       p.permission_key,
       r.id
FROM "roles" r
         CROSS JOIN (VALUES ('admin', 'files.read'),
                            ('admin', 'files.create'),
                            ('admin', 'files.delete'),
                            ('member', 'files.read'),
                            ('member', 'files.create'),
                            ('viewer', 'files.read'),
                            ('owner', 'files.read'),
                            ('owner', 'files.create'),
                            ('owner', 'files.delete')) AS p(role_key, permission_key)
WHERE r.is_system
  AND r.key = p.role_key
ON CONFLICT ("role_id", "permission_key") DO NOTHING;
