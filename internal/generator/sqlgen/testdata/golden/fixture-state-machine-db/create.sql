-- Generated PostgreSQL DDL for schema: fixture-state-machine-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE ticket (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  subject TEXT NOT NULL,
  state TEXT DEFAULT 'open' NOT NULL,
  review TEXT,
  escalation TEXT,
  account_status TEXT DEFAULT 'active' NOT NULL,
  sign_off JSONB,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  CONSTRAINT ck_ticket_state CHECK (state IN ('open', 'in_progress', 'resolved', 'closed')),
  CONSTRAINT ck_ticket_review CHECK (review IN ('RETRY', 'APPROVED', 'REJECTED')),
  CONSTRAINT ck_ticket_escalation CHECK (escalation IN ('raised', 'acknowledged'))
);

COMMENT ON TABLE ticket IS 'A support ticket. Its state starts open, may be reopened until it is
closed, and lists a move from open to open, which is no move. Its review
is null until a pass reviews the answer, and a retry clears it for the
next pass. Its escalation is null until it is raised, and once
acknowledged only returns to null. Its account''s status is an enum of
another service.';

-- Add foreign key constraints

-- Create indexes

-- Create the functions and triggers that guard a @stateMachine's column:
-- an update that makes a move the machine does not declare is refused,
-- whatever writes it

CREATE OR REPLACE FUNCTION ticket_account_status_transition_guard() RETURNS trigger AS $$
BEGIN
  -- The @stateMachine of Ticket.accountStatus (D117): an update that leaves the
  -- column's value is no move, and any other must be one the machine lists.
  IF NEW.account_status IS NOT DISTINCT FROM OLD.account_status THEN
    RETURN NEW;
  END IF;
  IF OLD.account_status IN ('closed') THEN
    RAISE EXCEPTION 'ticket.account_status is terminal at %: it cannot move to %', OLD.account_status, NEW.account_status
      USING ERRCODE = 'check_violation';
  END IF;
  IF (OLD.account_status = 'active' AND NEW.account_status IN ('suspended', 'closed'))
    OR (OLD.account_status = 'suspended' AND NEW.account_status IN ('active', 'closed'))
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'ticket.account_status cannot move from % to %', OLD.account_status, NEW.account_status
    USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_account_status_transition_guard
  BEFORE UPDATE OF account_status ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_account_status_transition_guard();

CREATE OR REPLACE FUNCTION ticket_escalation_transition_guard() RETURNS trigger AS $$
BEGIN
  -- The @stateMachine of Ticket.escalation (D117): an update that leaves the
  -- column's value is no move, and any other must be one the machine lists.
  IF NEW.escalation IS NOT DISTINCT FROM OLD.escalation THEN
    RETURN NEW;
  END IF;
  IF (OLD.escalation IS NULL AND NEW.escalation IN ('raised'))
    OR (OLD.escalation = 'raised' AND NEW.escalation IN ('acknowledged'))
    OR (OLD.escalation = 'acknowledged' AND NEW.escalation IS NULL)
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'ticket.escalation cannot move from % to %', OLD.escalation, NEW.escalation
    USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_escalation_transition_guard
  BEFORE UPDATE OF escalation ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_escalation_transition_guard();

CREATE OR REPLACE FUNCTION ticket_review_transition_guard() RETURNS trigger AS $$
BEGIN
  -- The @stateMachine of Ticket.review (D117): an update that leaves the
  -- column's value is no move, and any other must be one the machine lists.
  IF NEW.review IS NOT DISTINCT FROM OLD.review THEN
    RETURN NEW;
  END IF;
  IF OLD.review IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'ticket.review is terminal at %: it cannot move to %', OLD.review, NEW.review
      USING ERRCODE = 'check_violation';
  END IF;
  IF (OLD.review IS NULL AND NEW.review IN ('RETRY', 'APPROVED', 'REJECTED'))
    OR (OLD.review = 'RETRY' AND (NEW.review IS NULL OR NEW.review IN ('APPROVED', 'REJECTED')))
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'ticket.review cannot move from % to %', OLD.review, NEW.review
    USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_review_transition_guard
  BEFORE UPDATE OF review ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_review_transition_guard();

CREATE OR REPLACE FUNCTION ticket_state_transition_guard() RETURNS trigger AS $$
BEGIN
  -- The @stateMachine of Ticket.state (D117): an update that leaves the
  -- column's value is no move, and any other must be one the machine lists.
  IF NEW.state IS NOT DISTINCT FROM OLD.state THEN
    RETURN NEW;
  END IF;
  IF OLD.state IN ('closed') THEN
    RAISE EXCEPTION 'ticket.state is terminal at %: it cannot move to %', OLD.state, NEW.state
      USING ERRCODE = 'check_violation';
  END IF;
  IF (OLD.state = 'open' AND NEW.state IN ('open', 'in_progress', 'resolved', 'closed'))
    OR (OLD.state = 'in_progress' AND NEW.state IN ('resolved', 'open'))
    OR (OLD.state = 'resolved' AND NEW.state IN ('open', 'closed'))
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'ticket.state cannot move from % to %', OLD.state, NEW.state
    USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_state_transition_guard
  BEFORE UPDATE OF state ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_state_transition_guard();
