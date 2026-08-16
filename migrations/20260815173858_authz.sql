-- Create "authz_events" table
CREATE TABLE "authz_events" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "organization_id" uuid NULL,
  "actor_id" uuid NOT NULL,
  "subject_id" uuid NULL,
  "action" character varying(40) NOT NULL,
  "role_id" uuid NULL,
  "permission_key" character varying(100) NULL,
  "ip" inet NOT NULL,
  "user_agent" character varying(512) NULL,
  "detail" character varying(500) NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_authz_actor_time" to table: "authz_events"
CREATE INDEX "idx_authz_actor_time" ON "authz_events" ("actor_id", "created_at");
-- Create index "idx_authz_events_subject_id" to table: "authz_events"
CREATE INDEX "idx_authz_events_subject_id" ON "authz_events" ("subject_id");
-- Create index "idx_authz_org_time" to table: "authz_events"
CREATE INDEX "idx_authz_org_time" ON "authz_events" ("organization_id", "created_at");
-- Create "organizations" table
CREATE TABLE "organizations" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "deleted_at" timestamptz NULL,
  "is_protected" boolean NULL,
  "slug" character varying(63) NOT NULL,
  "name" character varying(100) NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_organizations_deleted_at" to table: "organizations"
CREATE INDEX "idx_organizations_deleted_at" ON "organizations" ("deleted_at");
-- Create index "idx_organizations_slug" to table: "organizations"
CREATE UNIQUE INDEX "idx_organizations_slug" ON "organizations" ("slug");
-- Create "roles" table
CREATE TABLE "roles" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "organization_id" uuid NOT NULL,
  "key" character varying(64) NOT NULL,
  "name" character varying(100) NOT NULL,
  "description" character varying(255) NULL,
  "is_system" boolean NOT NULL DEFAULT false,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_organizations_roles" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_role_org_key" to table: "roles"
CREATE UNIQUE INDEX "idx_role_org_key" ON "roles" ("organization_id", "key");
-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "locale" character varying(10) NULL;
-- Create "memberships" table
CREATE TABLE "memberships" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "organization_id" uuid NOT NULL,
  "status" character varying(20) NOT NULL,
  "invited_by" uuid NULL,
  "joined_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_memberships_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_organizations_memberships" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "chk_memberships_status" CHECK ((status)::text = ANY ((ARRAY['invited'::character varying, 'active'::character varying, 'suspended'::character varying])::text[]))
);
-- Create index "idx_membership_user_org" to table: "memberships"
CREATE UNIQUE INDEX "idx_membership_user_org" ON "memberships" ("user_id", "organization_id");
-- Create "membership_roles" table
CREATE TABLE "membership_roles" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "membership_id" uuid NOT NULL,
  "role_id" uuid NOT NULL,
  "granted_by" uuid NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_membership_roles_role" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_memberships_roles" FOREIGN KEY ("membership_id") REFERENCES "memberships" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_membership_role" to table: "membership_roles"
CREATE UNIQUE INDEX "idx_membership_role" ON "membership_roles" ("membership_id", "role_id");
-- Create "role_permissions" table
CREATE TABLE "role_permissions" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "role_id" uuid NOT NULL,
  "permission_key" character varying(100) NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_roles_permissions" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_role_permission" to table: "role_permissions"
CREATE UNIQUE INDEX "idx_role_permission" ON "role_permissions" ("role_id", "permission_key");
-- Create "role_translations" table
CREATE TABLE "role_translations" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "role_id" uuid NOT NULL,
  "locale" character varying(10) NOT NULL,
  "name" character varying(100) NOT NULL,
  "description" character varying(255) NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_roles_translations" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_role_translation" to table: "role_translations"
CREATE UNIQUE INDEX "idx_role_translation" ON "role_translations" ("role_id", "locale");
-- Create "user_system_roles" table
CREATE TABLE "user_system_roles" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "role_key" character varying(64) NOT NULL,
  "granted_by" uuid NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_user_system_roles_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_user_system_role" to table: "user_system_roles"
CREATE UNIQUE INDEX "idx_user_system_role" ON "user_system_roles" ("user_id", "role_key");
