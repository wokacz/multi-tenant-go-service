-- Modify "memberships" table
--
-- Atlas's schema diff emits ADD COLUMN ... NOT NULL in one step, which fails on
-- a database that already has memberships. The address is copied from the
-- account the row already points at before the column is required; invited
-- rows with a NULL user_id do not exist yet.
ALTER TABLE "memberships" ALTER COLUMN "user_id" DROP NOT NULL;
ALTER TABLE "memberships" ADD COLUMN "email" character varying(255) NULL;
UPDATE "memberships" AS m SET "email" = u.email FROM "users" AS u WHERE u.id = m.user_id;
ALTER TABLE "memberships" ALTER COLUMN "email" SET NOT NULL;
-- Create index "idx_membership_org_email" to table: "memberships"
CREATE UNIQUE INDEX "idx_membership_org_email" ON "memberships" ("organization_id", "email");
