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
