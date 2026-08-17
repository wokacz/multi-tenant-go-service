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
