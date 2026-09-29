-- Drop statements for schema: fixture-version-graph-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS uq_utensil_entity_ref CASCADE;
DROP INDEX IF EXISTS uq_tasting_entity_ref CASCADE;
DROP INDEX IF EXISTS uq_step_entity_ref CASCADE;
DROP INDEX IF EXISTS uq_recipe_snapshot_entry_entity CASCADE;
DROP INDEX IF EXISTS idx_recipe_snapshot_entry_entity_version CASCADE;
DROP INDEX IF EXISTS uq_recipe_release_root CASCADE;
DROP INDEX IF EXISTS uq_recipe_ref_root_name CASCADE;
DROP INDEX IF EXISTS uq_recipe_patch_entity CASCADE;
DROP INDEX IF EXISTS idx_recipe_patch_entity_version CASCADE;
DROP INDEX IF EXISTS uq_recipe_commit_root_sequence CASCADE;
DROP INDEX IF EXISTS uq_note_entity_ref CASCADE;
DROP INDEX IF EXISTS uq_ingredient_entity_ref CASCADE;
DROP INDEX IF EXISTS uq_cover_entity_ref CASCADE;

-- Drop history capture triggers and functions
DROP TRIGGER IF EXISTS trg_utensil_bump_version ON utensil;
DROP TRIGGER IF EXISTS trg_utensil_capture_history_write ON utensil;
DROP TRIGGER IF EXISTS trg_utensil_capture_history_delete ON utensil;
DROP FUNCTION IF EXISTS utensil_capture_history();
DROP TRIGGER IF EXISTS trg_tasting_bump_version ON tasting;
DROP TRIGGER IF EXISTS trg_tasting_capture_history_write ON tasting;
DROP TRIGGER IF EXISTS trg_tasting_capture_history_delete ON tasting;
DROP FUNCTION IF EXISTS tasting_capture_history();
DROP TRIGGER IF EXISTS trg_step_bump_version ON step;
DROP TRIGGER IF EXISTS trg_step_capture_history_write ON step;
DROP TRIGGER IF EXISTS trg_step_capture_history_delete ON step;
DROP FUNCTION IF EXISTS step_prune_history(INTEGER);
DROP FUNCTION IF EXISTS step_capture_history();
DROP TRIGGER IF EXISTS trg_recipe_release_bump_version ON recipe_release;
DROP TRIGGER IF EXISTS trg_recipe_release_capture_history_write ON recipe_release;
DROP TRIGGER IF EXISTS trg_recipe_release_capture_history_delete ON recipe_release;
DROP FUNCTION IF EXISTS recipe_release_capture_history();
DROP TRIGGER IF EXISTS trg_recipe_ref_bump_version ON recipe_ref;
DROP TRIGGER IF EXISTS trg_recipe_ref_capture_history_write ON recipe_ref;
DROP TRIGGER IF EXISTS trg_recipe_ref_capture_history_delete ON recipe_ref;
DROP FUNCTION IF EXISTS recipe_ref_capture_history();
DROP TRIGGER IF EXISTS trg_note_bump_version ON note;
DROP TRIGGER IF EXISTS trg_note_capture_history_write ON note;
DROP TRIGGER IF EXISTS trg_note_capture_history_delete ON note;
DROP FUNCTION IF EXISTS note_capture_history();
DROP TRIGGER IF EXISTS trg_ingredient_bump_version ON ingredient;
DROP TRIGGER IF EXISTS trg_ingredient_capture_history_write ON ingredient;
DROP TRIGGER IF EXISTS trg_ingredient_capture_history_delete ON ingredient;
DROP FUNCTION IF EXISTS ingredient_prune_history(INTEGER);
DROP FUNCTION IF EXISTS ingredient_capture_history();
DROP TRIGGER IF EXISTS trg_cover_bump_version ON cover;
DROP TRIGGER IF EXISTS trg_cover_capture_history_write ON cover;
DROP TRIGGER IF EXISTS trg_cover_capture_history_delete ON cover;
DROP FUNCTION IF EXISTS cover_capture_history();

-- Drop history indexes
DROP INDEX IF EXISTS idx_utensil_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_utensil_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_tasting_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_tasting_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_step_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_step_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_recipe_release_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_recipe_release_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_recipe_ref_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_recipe_ref_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_note_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_note_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_ingredient_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_ingredient_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_cover_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_cover_history_id_version CASCADE;

-- Drop history tables
DROP TABLE IF EXISTS utensil_history CASCADE;
DROP TABLE IF EXISTS tasting_history CASCADE;
DROP TABLE IF EXISTS step_history CASCADE;
DROP TABLE IF EXISTS recipe_release_history CASCADE;
DROP TABLE IF EXISTS recipe_ref_history CASCADE;
DROP TABLE IF EXISTS note_history CASCADE;
DROP TABLE IF EXISTS ingredient_history CASCADE;
DROP TABLE IF EXISTS cover_history CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS utensil CASCADE;
DROP TABLE IF EXISTS tasting CASCADE;
DROP TABLE IF EXISTS step CASCADE;
DROP TABLE IF EXISTS recipe_snapshot_entry CASCADE;
DROP TABLE IF EXISTS recipe_release CASCADE;
DROP TABLE IF EXISTS recipe_ref CASCADE;
DROP TABLE IF EXISTS recipe_patch CASCADE;
DROP TABLE IF EXISTS recipe_commit CASCADE;
DROP TABLE IF EXISTS recipe CASCADE;
DROP TABLE IF EXISTS note CASCADE;
DROP TABLE IF EXISTS ingredient CASCADE;
DROP TABLE IF EXISTS cover CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
