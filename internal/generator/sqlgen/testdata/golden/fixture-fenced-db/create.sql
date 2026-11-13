-- Generated PostgreSQL DDL for schema: fixture-fenced-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE courier (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL
);

COMMENT ON TABLE courier IS 'Who carries a delivery. It has no fence: a write of it advances nothing.';

CREATE TABLE delivery (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  address TEXT NOT NULL,
  courier_id UUID,
  plan_version BIGINT DEFAULT 0 NOT NULL,
  attempts BIGINT DEFAULT 0 NOT NULL,
  proof TEXT,
  run_version BIGINT DEFAULT 0 NOT NULL,
  note TEXT,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE delivery IS 'A delivery two parties write, neither waiting on the other. The planner
writes the address and the courier, a relation, covered as the key it
stores, and advances planVersion; the courier''s device writes attempts
and proof, and advances runVersion. A write of one party''s fields leaves
the other''s counter as it was, so neither fails the other''s compare.
No fence covers note or updatedAt: a write of them alone advances
neither counter.';

CREATE TABLE depot (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  capacity BIGINT NOT NULL,
  revision BIGINT DEFAULT 0 NOT NULL,
  deleted_at TIMESTAMPTZ
);

COMMENT ON TABLE depot IS 'A depot parcels wait in. A delete sets deletedAt, which the fence
covers, so the delete advances revision as a rename does; a write of
capacity alone does not.';

CREATE TABLE parcel (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  tracking_code TEXT NOT NULL,
  weight_grams BIGINT NOT NULL,
  note TEXT,
  version BIGINT DEFAULT 0 NOT NULL,
  UNIQUE (tracking_code)
);

COMMENT ON TABLE parcel IS 'A parcel at the desk. Its one fence lists no covers, so every update of
a row advances version by one, whatever it writes: a writer that read
version 4 writes where version is 4, and knows no other write came
between. An upsert by trackingCode updates a row the same way.';

-- Add foreign key constraints

ALTER TABLE delivery
  ADD CONSTRAINT fk_delivery_courier_id
  FOREIGN KEY (courier_id)
  REFERENCES courier(id)
  ON DELETE SET NULL;

-- Create indexes

-- Create the functions and triggers that hold a @fenced counter: an update
-- that does not advance it as its declaration says is refused, whatever
-- writes it

CREATE OR REPLACE FUNCTION delivery_plan_version_fence() RETURNS trigger AS $$
BEGIN
  -- The @fenced counter Delivery.planVersion (D118):
  -- an update that changes a column it covers sets it to the stored value
  -- plus one, and any other keeps it or adds one.
  IF ROW(NEW.address, NEW.courier_id) IS DISTINCT FROM ROW(OLD.address, OLD.courier_id) THEN
    IF NEW.plan_version IS DISTINCT FROM OLD.plan_version + 1 THEN
      RAISE EXCEPTION 'delivery.plan_version must advance from % to %, not to %', OLD.plan_version, OLD.plan_version + 1, NEW.plan_version
        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_plan_version_fence';
    END IF;
  ELSIF NEW.plan_version IS DISTINCT FROM OLD.plan_version THEN
    IF NEW.plan_version IS DISTINCT FROM OLD.plan_version + 1 THEN
      RAISE EXCEPTION 'delivery.plan_version must stay at % or advance to %, not move to %', OLD.plan_version, OLD.plan_version + 1, NEW.plan_version
        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_plan_version_fence';
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_delivery_plan_version_fence
  BEFORE UPDATE ON delivery
  FOR EACH ROW EXECUTE FUNCTION delivery_plan_version_fence();

CREATE OR REPLACE FUNCTION delivery_run_version_fence() RETURNS trigger AS $$
BEGIN
  -- The @fenced counter Delivery.runVersion (D118):
  -- an update that changes a column it covers sets it to the stored value
  -- plus one, and any other keeps it or adds one.
  IF ROW(NEW.attempts, NEW.proof) IS DISTINCT FROM ROW(OLD.attempts, OLD.proof) THEN
    IF NEW.run_version IS DISTINCT FROM OLD.run_version + 1 THEN
      RAISE EXCEPTION 'delivery.run_version must advance from % to %, not to %', OLD.run_version, OLD.run_version + 1, NEW.run_version
        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_run_version_fence';
    END IF;
  ELSIF NEW.run_version IS DISTINCT FROM OLD.run_version THEN
    IF NEW.run_version IS DISTINCT FROM OLD.run_version + 1 THEN
      RAISE EXCEPTION 'delivery.run_version must stay at % or advance to %, not move to %', OLD.run_version, OLD.run_version + 1, NEW.run_version
        USING ERRCODE = 'check_violation', CONSTRAINT = 'delivery_run_version_fence';
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_delivery_run_version_fence
  BEFORE UPDATE ON delivery
  FOR EACH ROW EXECUTE FUNCTION delivery_run_version_fence();

CREATE OR REPLACE FUNCTION depot_revision_fence() RETURNS trigger AS $$
BEGIN
  -- The @fenced counter Depot.revision (D118):
  -- an update that changes a column it covers sets it to the stored value
  -- plus one, and any other keeps it or adds one.
  IF ROW(NEW."name", NEW.deleted_at) IS DISTINCT FROM ROW(OLD."name", OLD.deleted_at) THEN
    IF NEW.revision IS DISTINCT FROM OLD.revision + 1 THEN
      RAISE EXCEPTION 'depot.revision must advance from % to %, not to %', OLD.revision, OLD.revision + 1, NEW.revision
        USING ERRCODE = 'check_violation', CONSTRAINT = 'depot_revision_fence';
    END IF;
  ELSIF NEW.revision IS DISTINCT FROM OLD.revision THEN
    IF NEW.revision IS DISTINCT FROM OLD.revision + 1 THEN
      RAISE EXCEPTION 'depot.revision must stay at % or advance to %, not move to %', OLD.revision, OLD.revision + 1, NEW.revision
        USING ERRCODE = 'check_violation', CONSTRAINT = 'depot_revision_fence';
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_depot_revision_fence
  BEFORE UPDATE ON depot
  FOR EACH ROW EXECUTE FUNCTION depot_revision_fence();

CREATE OR REPLACE FUNCTION parcel_version_fence() RETURNS trigger AS $$
BEGIN
  -- The @fenced counter Parcel.version (D118):
  -- every update of a row sets it to the stored value plus one.
  IF NEW.version IS DISTINCT FROM OLD.version + 1 THEN
    RAISE EXCEPTION 'parcel.version must advance from % to %, not to %', OLD.version, OLD.version + 1, NEW.version
      USING ERRCODE = 'check_violation', CONSTRAINT = 'parcel_version_fence';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_parcel_version_fence
  BEFORE UPDATE ON parcel
  FOR EACH ROW EXECUTE FUNCTION parcel_version_fence();
