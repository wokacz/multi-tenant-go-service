-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "two_factor_enabled" boolean NOT NULL DEFAULT false;
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
