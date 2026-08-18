-- Baseline: the whole schema in one migration.
--
-- The eight migrations that came before this were squashed into it. Nothing is
-- deployed from this repository yet, so the incremental history described steps
-- no database had taken in that order anyway, and eight files to read to learn
-- the shape of one schema is worse than one.
--
-- Two things went with them, deliberately:
--
--   * the data backfills — the UPDATE that copied users.email onto memberships
--     when that column was added. A baseline starts from no rows, so there is
--     nothing to backfill.
--   * the intermediate states — memberships.user_id was NOT NULL and then made
--     nullable. Here it is simply nullable, which is what the model says.
--
-- Consequence for anyone holding a database built from the old history: Atlas
-- tracks applied versions, and those versions no longer exist. Drop the database
-- and apply this instead. See guides/003.

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
-- Create "users" table
CREATE TABLE "users" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "deleted_at" timestamptz NULL,
  "is_protected" boolean NULL,
  "name" character varying(100) NOT NULL,
  "email" character varying(255) NOT NULL,
  "password_hash" character varying(255) NOT NULL,
  "session_epoch" bigint NOT NULL DEFAULT 0,
  "two_factor_enabled" boolean NOT NULL DEFAULT false,
  "suspended_at" timestamptz NULL,
  "locale" character varying(10) NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_users_deleted_at" to table: "users"
CREATE INDEX "idx_users_deleted_at" ON "users" ("deleted_at");
-- Create index "idx_users_email" to table: "users"
CREATE UNIQUE INDEX "idx_users_email" ON "users" ("email") WHERE (deleted_at IS NULL);
-- Create "devices" table
CREATE TABLE "devices" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "fingerprint" character varying(64) NOT NULL,
  "label" character varying(100) NULL,
  "user_agent" character varying(512) NULL,
  "last_seen_at" timestamptz NULL,
  "last_ip" inet NULL,
  "trusted_at" timestamptz NULL,
  "revoked_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_users_devices" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_device_user_fp" to table: "devices"
CREATE UNIQUE INDEX "idx_device_user_fp" ON "devices" ("user_id", "fingerprint");
-- Create index "idx_devices_last_seen_at" to table: "devices"
CREATE INDEX "idx_devices_last_seen_at" ON "devices" ("last_seen_at");
-- Create "email_changes" table
CREATE TABLE "email_changes" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "new_email" character varying(255) NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "attempts" bigint NOT NULL,
  "consumed_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_email_changes_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_email_changes_expires_at" to table: "email_changes"
CREATE INDEX "idx_email_changes_expires_at" ON "email_changes" ("expires_at");
-- Create index "idx_email_changes_user_id" to table: "email_changes"
CREATE INDEX "idx_email_changes_user_id" ON "email_changes" ("user_id");
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
CREATE UNIQUE INDEX "idx_organizations_slug" ON "organizations" ("slug") WHERE (deleted_at IS NULL);
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
-- Create "invitations" table
CREATE TABLE "invitations" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "organization_id" uuid NOT NULL,
  "email" character varying(255) NOT NULL,
  "token_hash" character varying(64) NOT NULL,
  "invited_by" uuid NULL,
  "expires_at" timestamptz NOT NULL,
  "accepted_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_invitations_organization" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_invitation_org_email" to table: "invitations"
CREATE UNIQUE INDEX "idx_invitation_org_email" ON "invitations" ("organization_id", "email");
-- Create index "idx_invitations_expires_at" to table: "invitations"
CREATE INDEX "idx_invitations_expires_at" ON "invitations" ("expires_at");
-- Create index "idx_invitations_token_hash" to table: "invitations"
CREATE UNIQUE INDEX "idx_invitations_token_hash" ON "invitations" ("token_hash");
-- Create "invitation_roles" table
CREATE TABLE "invitation_roles" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "invitation_id" uuid NOT NULL,
  "role_id" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_invitation_roles_role" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_invitations_roles" FOREIGN KEY ("invitation_id") REFERENCES "invitations" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_invitation_role" to table: "invitation_roles"
CREATE UNIQUE INDEX "idx_invitation_role" ON "invitation_roles" ("invitation_id", "role_id");
-- Create "login_events" table
CREATE TABLE "login_events" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "device_id" uuid NULL,
  "ip" inet NOT NULL,
  "user_agent" character varying(512) NULL,
  "outcome" character varying(20) NOT NULL,
  "country" character varying(2) NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_users_login_events" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "chk_login_events_outcome" CHECK ((outcome)::text = ANY ((ARRAY['success'::character varying, 'bad_password'::character varying, 'mfa_failed'::character varying, 'locked'::character varying])::text[]))
);
-- Create index "idx_login_device_time" to table: "login_events"
CREATE INDEX "idx_login_device_time" ON "login_events" ("device_id", "created_at");
-- Create index "idx_login_events_ip" to table: "login_events"
CREATE INDEX "idx_login_events_ip" ON "login_events" ("ip");
-- Create index "idx_login_user_time" to table: "login_events"
CREATE INDEX "idx_login_user_time" ON "login_events" ("user_id", "created_at");
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
  CONSTRAINT "chk_memberships_status" CHECK ((status)::text = ANY ((ARRAY['active'::character varying, 'suspended'::character varying])::text[]))
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
-- Create "password_resets" table
CREATE TABLE "password_resets" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "attempts" bigint NOT NULL,
  "consumed_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_password_resets_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_password_resets_expires_at" to table: "password_resets"
CREATE INDEX "idx_password_resets_expires_at" ON "password_resets" ("expires_at");
-- Create index "idx_password_resets_user_id" to table: "password_resets"
CREATE INDEX "idx_password_resets_user_id" ON "password_resets" ("user_id");
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
-- Create "two_factor_challenges" table
CREATE TABLE "two_factor_challenges" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  "user_id" uuid NOT NULL,
  "device_id" uuid NOT NULL,
  "code_hash" character varying(64) NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "attempts" bigint NOT NULL,
  "consumed_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_two_factor_challenges_device" FOREIGN KEY ("device_id") REFERENCES "devices" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_two_factor_challenges_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_two_factor_challenges_device_id" to table: "two_factor_challenges"
CREATE INDEX "idx_two_factor_challenges_device_id" ON "two_factor_challenges" ("device_id");
-- Create index "idx_two_factor_challenges_expires_at" to table: "two_factor_challenges"
CREATE INDEX "idx_two_factor_challenges_expires_at" ON "two_factor_challenges" ("expires_at");
-- Create index "idx_two_factor_challenges_user_id" to table: "two_factor_challenges"
CREATE INDEX "idx_two_factor_challenges_user_id" ON "two_factor_challenges" ("user_id");
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
