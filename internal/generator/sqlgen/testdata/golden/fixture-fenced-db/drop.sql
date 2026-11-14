-- Drop statements for schema: fixture-fenced-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that hold a @fenced counter
DROP TRIGGER IF EXISTS trg_delivery_plan_version_fence ON delivery;
DROP FUNCTION IF EXISTS delivery_plan_version_fence();
DROP TRIGGER IF EXISTS trg_delivery_run_version_fence ON delivery;
DROP FUNCTION IF EXISTS delivery_run_version_fence();
DROP TRIGGER IF EXISTS trg_depot_revision_fence ON depot;
DROP FUNCTION IF EXISTS depot_revision_fence();
DROP TRIGGER IF EXISTS trg_parcel_version_fence ON parcel;
DROP FUNCTION IF EXISTS parcel_version_fence();

-- Drop indexes

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS parcel CASCADE;
DROP TABLE IF EXISTS depot CASCADE;
DROP TABLE IF EXISTS delivery CASCADE;
DROP TABLE IF EXISTS courier CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
