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
  PRIMARY KEY ("id")
);
-- Create index "idx_users_deleted_at" to table: "users"
CREATE INDEX "idx_users_deleted_at" ON "users" ("deleted_at");
-- Create index "idx_users_email" to table: "users"
CREATE UNIQUE INDEX "idx_users_email" ON "users" ("email");
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
