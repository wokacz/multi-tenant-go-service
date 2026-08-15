-- Modify "users" table
ALTER TABLE "users" ADD COLUMN "session_epoch" bigint NOT NULL DEFAULT 0;
