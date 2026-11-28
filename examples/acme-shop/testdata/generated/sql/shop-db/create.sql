-- Generated PostgreSQL DDL for schema: shop-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;


-- Create tables (without foreign key constraints)

CREATE TABLE checkout (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  customer_id UUID NOT NULL,
  lines JSONB NOT NULL,
  counter_id UUID,
  pickup_at TIMESTAMPTZ,
  address JSONB,
  holds_state TEXT,
  holds_total JSONB,
  payments_due BIGINT,
  payments_authorized BIGINT,
  payments_captured BIGINT,
  payments_refunded BIGINT,
  payments_outstanding BIGINT,
  payments_overpaid BIGINT,
  payments_currency TEXT,
  payments_state TEXT,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  number_seq BIGINT,
  number TEXT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE checkout IS 'A shopper''s checkout. shop-cart keeps it in cart while the shopper fills
it; shop-orders'' startCheckout moves it to held, which holds each line''s
units and the pickup slot at the counter, all of them or none;
payCheckout opens its payment with acmepay, and once acmepay''s capture
is recorded shop-orders moves it to placed, which Payments allows only
once it is paid, which commits what it holds, and writes its Order row.
A checkout still held fifteen minutes on expires, through the deadline
shop-orders'' behavior worker runs, and lets go of what it holds, unless
a payment is in flight, which holds it until the two-hour backstop. A
capture that arrives after it expired is refunded.';

CREATE TABLE checkout_holds_items (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  "key" TEXT NOT NULL,
  hold TEXT,
  item BIGINT,
  supply_schema TEXT,
  supply_id TEXT,
  quantity BIGINT,
  start_ms BIGINT,
  end_ms BIGINT,
  state TEXT,
  unit_price BIGINT,
  currency TEXT,
  taken_at BIGINT
);

COMMENT ON TABLE checkout_holds_items IS 'On a table, each item the claim has taken, keyed by its hold and index: the supply it names, what it asks, its state and the unit price the supply held it at, and the Workflow entry it was taken at. An item its fields ask that it has not taken is no row; the items field shows it pending.';

CREATE TABLE checkout_number_counts (
  scope TEXT NOT NULL PRIMARY KEY,
  last BIGINT,
  counting TEXT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE checkout_number_counts IS 'On a table, one row per count, kept for the table as a whole: the last number it gave and the config it counted under. A delete of a row leaves it where it was, so a delete leaves a gap; a row whose config is not the use''s counts from 1 again.';

CREATE TABLE checkout_payments_actions (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  "action" TEXT NOT NULL,
  seq BIGINT,
  kind TEXT,
  attempt TEXT,
  refund TEXT,
  provider TEXT,
  amount BIGINT,
  currency TEXT,
  at BIGINT,
  done_at BIGINT
);

COMMENT ON TABLE checkout_payments_actions IS 'On a table, each thing the provider''s adapter is to do for the row (create, capture, void, refund), keyed by its id, waiting until an event for it is recorded.';

CREATE TABLE checkout_payments_attempts (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  attempt TEXT NOT NULL,
  seq BIGINT,
  idempotency_key TEXT,
  provider TEXT,
  provider_ref TEXT,
  amount BIGINT,
  currency TEXT,
  due BIGINT,
  status TEXT,
  authorized BIGINT,
  captured BIGINT,
  brand TEXT,
  last4 TEXT,
  return_url TEXT,
  opened_at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE checkout_payments_attempts IS 'On a table, each payment attempt the row opened, keyed by its id: its provider and the provider''s reference, its amount, currency and what was due when it opened, its status, what is authorized and captured of it, a card''s brand and last four digits, and when it opened and last moved.';

CREATE TABLE checkout_payments_events (
  event TEXT NOT NULL PRIMARY KEY,
  provider TEXT,
  event_id TEXT,
  row_key TEXT,
  kind TEXT,
  amount BIGINT,
  currency TEXT,
  at BIGINT,
  attempt TEXT,
  provider_ref TEXT,
  refund TEXT,
  applied BOOLEAN,
  metadata JSONB,
  recorded_at BIGINT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE checkout_payments_events IS 'On a table, every provider event recorded, kept for the table as a whole and keyed by its provider and event id, so an event applies once per table and provider: the row it was recorded on, its normalised fields alone, and whether it moved anything. A delete of the row leaves it, so a redelivery is still a repeat.';

CREATE TABLE checkout_payments_issues (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  refund TEXT NOT NULL,
  seq BIGINT,
  of_schema TEXT,
  of_id TEXT,
  idempotency_key TEXT,
  amount BIGINT,
  currency TEXT,
  status TEXT,
  refunded BIGINT,
  entered_at BIGINT,
  at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE checkout_payments_issues IS 'On a refund row (refundOf), each refund it asked of the row it refunds, keyed by the refund''s id: its key, amount and currency, its status as the provider settled it, and the Workflow entry it was asked at.';

CREATE TABLE checkout_payments_refunds (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  refund TEXT NOT NULL,
  seq BIGINT,
  idempotency_key TEXT,
  attempt TEXT,
  provider TEXT,
  amount BIGINT,
  currency TEXT,
  reason TEXT,
  status TEXT,
  refunded BIGINT,
  origin_schema TEXT,
  origin_id TEXT,
  at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE checkout_payments_refunds IS 'On a table, each refund asked of the row, keyed by its id: the attempt it draws on, its amount and reason, its status and what was refunded, and the refund row that asked it, if one did.';

CREATE TABLE checkout_timers_deadlines (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  base_at TIMESTAMPTZ,
  armed_from TEXT,
  due_at TIMESTAMPTZ,
  fired BOOLEAN,
  fired_at TIMESTAMPTZ,
  outcome TEXT
);

COMMENT ON TABLE checkout_timers_deadlines IS 'One row per deadline started on the row, keyed by the deadline''s name: the time it starts from, the fact it started from, when it is due (null once until cancelled it), whether and when it fired, and what its then met.';

CREATE TABLE collection_point (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL,
  CONSTRAINT ck_collection_point_name CHECK (length("name") <= 200)
);

COMMENT ON TABLE collection_point IS 'A counter a shopper collects an order from, which opens fifteen-minute
pickup slots in London''s time, four shoppers at a time, from two hours
ahead to sixty days ahead, with five minutes after each to clear it.';

CREATE TABLE collection_point_calendar_bookings (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  collection_point_id UUID NOT NULL,
  record TEXT NOT NULL,
  holder_schema TEXT,
  holder_id TEXT,
  hold TEXT,
  item BIGINT,
  start_ms BIGINT,
  end_ms BIGINT,
  state TEXT,
  unit_price BIGINT,
  currency TEXT,
  at TIMESTAMPTZ,
  retired BOOLEAN
);

COMMENT ON TABLE collection_point_calendar_bookings IS 'On a table, the supply''s record of each claim item that holds or held time of the row, keyed by the claim''s type and key, the hold and the item''s index: the interval it asked, its state, which a call moves only forward, so a call made again changes nothing, and when it last moved. A deleted claim''s records retire under a key of their own, its committed time still booked, so a claim created again with its key holds afresh.';

CREATE TABLE collection_point_calendar_closures (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  collection_point_id UUID NOT NULL,
  closure TEXT NOT NULL,
  start_ms BIGINT,
  end_ms BIGINT,
  reason TEXT,
  by TEXT,
  at TIMESTAMPTZ
);

COMMENT ON TABLE collection_point_calendar_closures IS 'On a table, the row''s closures, keyed by their name, closure-1 onward: when each starts and ends, why, who made it and when.';

CREATE TABLE collection_point_calendar_settings (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  collection_point_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  value JSONB
);

COMMENT ON TABLE collection_point_calendar_settings IS 'On a table, what setOpeningHours and addClosure set of the row, keyed by name: hours, the row''s own weekly hours; timeZone, its own zone; closuresMade, how many closures it has made, which names the next. A name with no row follows the config.';

CREATE TABLE inventory (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  sku TEXT NOT NULL,
  price_cents BIGINT NOT NULL,
  reorder_at BIGINT,
  stock_on_hand BIGINT,
  stock_held BIGINT,
  _version BIGINT DEFAULT 1 NOT NULL,
  CONSTRAINT ck_inventory_sku CHECK (length(sku) <= 64)
);

COMMENT ON TABLE inventory IS 'The units the warehouse holds of a product: one row per product, whose
key is the product''s id. shop-stock''s staff routes receive and count
them, and a checkout holds them at the price they sell for, in pounds.
Its available units at or below reorderAt make it low, which
shop-stock''s job OrderRestocks finds.';

CREATE TABLE inventory_stock_holds (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  inventory_id UUID NOT NULL,
  record TEXT NOT NULL,
  holder_schema TEXT,
  holder_id TEXT,
  hold TEXT,
  item BIGINT,
  quantity BIGINT,
  state TEXT,
  unit_price BIGINT,
  currency TEXT,
  at TIMESTAMPTZ,
  retired BOOLEAN
);

COMMENT ON TABLE inventory_stock_holds IS 'On a table, the supply''s record of each claim item it holds or held, keyed by the claim''s type and key, the hold and the item''s index: what the item asked, its state, which a call moves only forward, so a call made again changes nothing, and the price it was held at. A deleted claim''s records retire under a key of their own: what it held is released, what it committed stays sold, and a claim created again with its key holds afresh.';

CREATE TABLE inventory_stock_movements (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  inventory_id UUID NOT NULL,
  seq TEXT NOT NULL,
  kind TEXT,
  delta BIGINT,
  on_hand BIGINT,
  reason TEXT,
  reference TEXT,
  holder_schema TEXT,
  holder_id TEXT,
  hold TEXT,
  item BIGINT,
  actor TEXT,
  at TIMESTAMPTZ
);

COMMENT ON TABLE inventory_stock_movements IS 'On a table, every change of what is on hand, append-only, keyed by its sequence from 1: its kind, its delta, what was on hand after it, its reason and reference, the claim and item it was for, who made it and when. onHand always equals the sum of the deltas.';

CREATE TABLE oauth_client (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  client_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  "name" TEXT NOT NULL,
  uri TEXT,
  redirect_uris TEXT[] DEFAULT '{}' NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  refreshed_at TIMESTAMPTZ,
  fresh_until TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  UNIQUE (client_id),
  CONSTRAINT ck_oauth_client_client_id CHECK (length(client_id) >= 1)
);

COMMENT ON TABLE oauth_client IS 'An OAuth client: a metadata document''s, cached, or one that registered.';

CREATE TABLE oauth_code (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  client_id UUID NOT NULL,
  user_id UUID,
  grant_id UUID,
  redirect_uri TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  state TEXT,
  scope TEXT,
  resource TEXT NOT NULL,
  nonce TEXT NOT NULL,
  code_hash VARCHAR(64),
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  used_at TIMESTAMPTZ,
  UNIQUE (code_hash)
);

COMMENT ON TABLE oauth_code IS 'An OAuth authorization request: pending while its user consents, then the code its client redeems once.';

CREATE TABLE oauth_grant (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  client_id UUID NOT NULL,
  permissions TEXT[],
  resource TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ
);

COMMENT ON TABLE oauth_grant IS 'An OAuth grant: the user who gave it, the client that holds it, the permissions it narrows them to, and when it ends.';

CREATE TABLE oauth_token (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  grant_id UUID NOT NULL,
  kind TEXT NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash)
);

COMMENT ON TABLE oauth_token IS 'An OAuth access or refresh token: the grant that issued it, the SHA-256 of its text, and when it ends.';

CREATE TABLE "order" (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  customer_id UUID NOT NULL,
  status TEXT DEFAULT 'placed' NOT NULL,
  placed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  shipping_address JSONB NOT NULL,
  cancel_reason TEXT,
  checkout_id UUID,
  total_cents BIGINT NOT NULL,
  UNIQUE (checkout_id),
  CONSTRAINT ck_order_status CHECK (status IN ('placed', 'fulfilled', 'shipped', 'cancelled')),
  CONSTRAINT ck_order_cancel_reason CHECK (length(cancel_reason) <= 500)
);

COMMENT ON TABLE "order" IS 'A shopper''s order. Staff look orders up by customer, newest first. An
order put back to placed, by staff or by a script in psql, is fulfilled
again: the database''s trigger enqueues its OrderPlaced, whatever wrote
the row.';

CREATE TABLE order_line (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  product_id UUID NOT NULL,
  quantity BIGINT NOT NULL,
  unit_price_cents BIGINT NOT NULL
);

COMMENT ON TABLE order_line IS 'One product on an order, at the price the shopper paid.';

CREATE TABLE product (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  sku CITEXT NOT NULL,
  "name" VARCHAR(80) NOT NULL,
  price_cents BIGINT NOT NULL,
  in_stock BOOLEAN DEFAULT true NOT NULL,
  image_object TEXT,
  UNIQUE (sku),
  CONSTRAINT ck_product_image_object CHECK (length(image_object) <= 1024)
);

-- Add search_text generated column for trigram search
ALTER TABLE product ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE("name", '')
) STORED;

COMMENT ON TABLE product IS 'Something the shop sells. Shoppers find a product by its name.';

CREATE TABLE refund (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  checkout_id UUID NOT NULL,
  amount_cents BIGINT NOT NULL,
  reason TEXT NOT NULL,
  created_by UUID NOT NULL,
  payments_due BIGINT,
  payments_authorized BIGINT,
  payments_captured BIGINT,
  payments_refunded BIGINT,
  payments_outstanding BIGINT,
  payments_overpaid BIGINT,
  payments_currency TEXT,
  payments_state TEXT,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  number_seq BIGINT,
  number TEXT,
  _version BIGINT DEFAULT 1 NOT NULL,
  CONSTRAINT ck_refund_reason CHECK (length(reason) <= 500)
);

COMMENT ON TABLE refund IS 'A refund of a placed checkout, which two staff who hold refunds.approve
sign off before it is issued: entering approved asks the checkout''s
Payments for the refund, which shop-orders'' job SettlePayments makes at
acmepay and records on the checkout, which settles it here; the job then
moves it to issued.';

CREATE TABLE refund_approvals_sign_offs (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  refund_id UUID NOT NULL,
  number TEXT NOT NULL,
  state TEXT,
  subject TEXT,
  kind TEXT,
  acting_for TEXT,
  acting_for_kind TEXT,
  agent TEXT,
  signed_in_at TIMESTAMPTZ,
  user_verified BOOLEAN,
  held JSONB,
  "values" JSONB,
  note TEXT,
  at TIMESTAMPTZ,
  consumed_at TIMESTAMPTZ
);

COMMENT ON TABLE refund_approvals_sign_offs IS 'One row per sign-off on the row, keyed by its number, the order it was given in: the gated state, the caller who signed (its subject and kind, whom it acted for and its run''s Agent), the assurance its host reported, the permissions it held and the values of the fields the gate''s requirements reset on, why, when, and when it was consumed, absent while it counts. A withdrawal deletes its row; consumed ones stay for listSignOffs.';

CREATE TABLE refund_number_counts (
  scope TEXT NOT NULL PRIMARY KEY,
  last BIGINT,
  counting TEXT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE refund_number_counts IS 'On a table, one row per count, kept for the table as a whole: the last number it gave and the config it counted under. A delete of a row leaves it where it was, so a delete leaves a gap; a row whose config is not the use''s counts from 1 again.';

CREATE TABLE refund_payments_actions (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  refund_row_id UUID NOT NULL,
  "action" TEXT NOT NULL,
  seq BIGINT,
  kind TEXT,
  attempt TEXT,
  refund TEXT,
  provider TEXT,
  amount BIGINT,
  currency TEXT,
  at BIGINT,
  done_at BIGINT
);

COMMENT ON TABLE refund_payments_actions IS 'On a table, each thing the provider''s adapter is to do for the row (create, capture, void, refund), keyed by its id, waiting until an event for it is recorded.';

CREATE TABLE refund_payments_attempts (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  refund_id UUID NOT NULL,
  attempt TEXT NOT NULL,
  seq BIGINT,
  idempotency_key TEXT,
  provider TEXT,
  provider_ref TEXT,
  amount BIGINT,
  currency TEXT,
  due BIGINT,
  status TEXT,
  authorized BIGINT,
  captured BIGINT,
  brand TEXT,
  last4 TEXT,
  return_url TEXT,
  opened_at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE refund_payments_attempts IS 'On a table, each payment attempt the row opened, keyed by its id: its provider and the provider''s reference, its amount, currency and what was due when it opened, its status, what is authorized and captured of it, a card''s brand and last four digits, and when it opened and last moved.';

CREATE TABLE refund_payments_events (
  event TEXT NOT NULL PRIMARY KEY,
  provider TEXT,
  event_id TEXT,
  row_key TEXT,
  kind TEXT,
  amount BIGINT,
  currency TEXT,
  at BIGINT,
  attempt TEXT,
  provider_ref TEXT,
  refund TEXT,
  applied BOOLEAN,
  metadata JSONB,
  recorded_at BIGINT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE refund_payments_events IS 'On a table, every provider event recorded, kept for the table as a whole and keyed by its provider and event id, so an event applies once per table and provider: the row it was recorded on, its normalised fields alone, and whether it moved anything. A delete of the row leaves it, so a redelivery is still a repeat.';

CREATE TABLE refund_payments_issues (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  refund_row_id UUID NOT NULL,
  refund TEXT NOT NULL,
  seq BIGINT,
  of_schema TEXT,
  of_id TEXT,
  idempotency_key TEXT,
  amount BIGINT,
  currency TEXT,
  status TEXT,
  refunded BIGINT,
  entered_at BIGINT,
  at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE refund_payments_issues IS 'On a refund row (refundOf), each refund it asked of the row it refunds, keyed by the refund''s id: its key, amount and currency, its status as the provider settled it, and the Workflow entry it was asked at.';

CREATE TABLE refund_payments_refunds (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  refund_row_id UUID NOT NULL,
  refund TEXT NOT NULL,
  seq BIGINT,
  idempotency_key TEXT,
  attempt TEXT,
  provider TEXT,
  amount BIGINT,
  currency TEXT,
  reason TEXT,
  status TEXT,
  refunded BIGINT,
  origin_schema TEXT,
  origin_id TEXT,
  at BIGINT,
  updated_at BIGINT
);

COMMENT ON TABLE refund_payments_refunds IS 'On a table, each refund asked of the row, keyed by its id: the attempt it draws on, its amount and reason, its status and what was refunded, and the refund row that asked it, if one did.';

CREATE TABLE restock_request (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  product_id UUID NOT NULL,
  status TEXT DEFAULT 'open' NOT NULL,
  quantity BIGINT NOT NULL,
  open_product_id UUID,
  ordered_at TIMESTAMPTZ,
  CONSTRAINT ck_restock_request_status CHECK (status IN ('open', 'ordered'))
);

COMMENT ON TABLE restock_request IS 'A request to the supplier for more of a product: shop-stock''s worker
ReorderStock files one when a sale runs the product low, and its job
OrderRestocks orders each open one. A product has at most one
open request: openProductId is the product while the request is open and
null once it is ordered, and the unique index over it admits one row per
product, since a unique index lets nulls repeat.';

CREATE TABLE review (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  product_id UUID NOT NULL,
  author_id UUID NOT NULL,
  rating DOUBLE PRECISION NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  deleted_at TIMESTAMPTZ,
  deleted_by UUID,
  CONSTRAINT ck_review_rating CHECK (rating >= 1 AND rating <= 5),
  CONSTRAINT ck_review_title CHECK (length(title) <= 120)
);

-- Add search_text generated column for trigram search
ALTER TABLE review ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE(title, '') || ' ' || COALESCE(body, '')
) STORED;

COMMENT ON TABLE review IS 'A shopper''s review of a product. Shoppers search reviews by their text,
and each shopper reviews a product once. A moderator hides a review by
deleting it; the row stays, marked deleted.';

CREATE TABLE role (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" CITEXT NOT NULL,
  permissions TEXT[] DEFAULT '{}' NOT NULL,
  UNIQUE ("name")
);

COMMENT ON TABLE role IS 'A named set of permissions a user is granted: shoppers hold orders.read
and orders.write, to place their orders and read and cancel them, and
staff products, to add products, and orders.read, orders.write and
orders.any, or simply orders, to read and cancel every customer''s
orders. orders.any widens what the other two allow and opens no route
alone. Users hold roles through the UserRoleGrant table the build adds.';

CREATE TABLE "session" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_seen_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash)
);

COMMENT ON TABLE "session" IS 'A signed-in session: the user it signs in, the SHA-256 of its token, and when it ends.';

CREATE TABLE ticket (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  customer_id UUID NOT NULL,
  subject TEXT NOT NULL,
  body TEXT,
  order_id TEXT,
  category TEXT,
  priority TEXT,
  summary TEXT,
  provenance_in_review BOOLEAN,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL,
  CONSTRAINT ck_ticket_subject CHECK (length(subject) <= 200),
  CONSTRAINT ck_ticket_body CHECK (length(body) <= 10000),
  CONSTRAINT ck_ticket_order_id CHECK (length(order_id) <= 64),
  CONSTRAINT ck_ticket_category CHECK (category IN ('orders', 'payments', 'delivery', 'product', 'other')),
  CONSTRAINT ck_ticket_priority CHECK (priority IN ('low', 'normal', 'urgent')),
  CONSTRAINT ck_ticket_summary CHECK (length(summary) <= 500)
);

COMMENT ON TABLE ticket IS 'A shopper''s support ticket, which a model triages and a person confirms
(D73, D74, D146): shop-cart''s deriver TriageTickets fills its category,
priority and summary from its subject and body while it is new or in
triage. A category or priority the model is less than 0.8 sure of waits
in review, which holds the ticket from assigned until support staff
(support.review) accept or correct it and move it on. A ticket the model
is sure of is assigned with no person: the Reactions rules hear its
category and its priority settle and move it, once both are there, the
state''s required fields, and out of review. The summary is derived in
every state, so it follows the body. shop-cart''s behavior worker
TicketReactions runs the rules.';

CREATE TABLE ticket_derived_derivations (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  field TEXT NOT NULL,
  source_hash VARCHAR(64),
  settled_hash VARCHAR(64),
  model TEXT,
  confidence DOUBLE PRECISION,
  settled_at TIMESTAMPTZ,
  settled_by TEXT,
  overridden_by TEXT,
  overridden_at TIMESTAMPTZ,
  attempts BIGINT,
  failure JSONB,
  forced BOOLEAN
);

COMMENT ON TABLE ticket_derived_derivations IS 'On a table, one row per derived field of the row, keyed by the field''s name: the hash its value is derived under, the hash of its last settled value with its model, confidence, settler and time, the override a write gave it, its failed attempts and last failure, and whether rederive forced it. Every write of the row keeps them in line; a delete of the row deletes them.';

CREATE TABLE ticket_provenance_records (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  field TEXT NOT NULL,
  source TEXT,
  by_subject TEXT,
  for_subject TEXT,
  written_at TIMESTAMPTZ,
  confidence DOUBLE PRECISION,
  review TEXT,
  reviewed_by TEXT,
  reviewed_at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_provenance_records IS 'On a table, one row per recorded field of the row, keyed by the field''s name: the last write''s source, who made it and for whom, when, the confidence its attribution gave, and its review. Every write of the row keeps them in line; a delete of the row deletes them.';

CREATE TABLE "user" (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  email CITEXT NOT NULL,
  "name" VARCHAR(80) NOT NULL,
  UNIQUE (email)
);

COMMENT ON TABLE "user" IS 'A person who can sign in to the shop, with their email and a password.
The User trait makes the table the core user model''s (D50): the build
adds the Session and UserCredential tables beside it, and both APIs sign
users in and out with the identity runtime. The trait is imported as
UserTrait, since the table is named User too.';

CREATE TABLE user_api_key (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  permissions TEXT[],
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash),
  CONSTRAINT ck_user_api_key_name CHECK (length("name") >= 1 AND length("name") <= 100)
);

COMMENT ON TABLE user_api_key IS 'An API key: the user it calls as, its name, the SHA-256 of its token, and when it ends.';

CREATE TABLE user_credential (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  password_hash TEXT NOT NULL,
  password_changed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  disabled_at TIMESTAMPTZ,
  pending_at TIMESTAMPTZ,
  UNIQUE (user_id)
);

COMMENT ON TABLE user_credential IS 'A user''s password hash, kept apart from the user row.';

CREATE TABLE user_role_grant (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  role_id UUID NOT NULL,
  granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE user_role_grant IS 'A role a user holds.';

CREATE TABLE checkout_change_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  write TEXT NOT NULL,
  at BIGINT NOT NULL,
  before JSONB,
  "after" JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE checkout_change_queue IS 'The writes of Checkout''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE checkout_deadline_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  "name" TEXT NOT NULL,
  at BIGINT NOT NULL,
  payload JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE checkout_deadline_queue IS 'The deadlines Checkout''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

CREATE TABLE order_placed_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE order_placed_queue IS 'Placed with each order, in the transaction that writes it, and by the
trigger on an order put back to placed: shop-orders'' worker FulfilOrders
handles each message, and a message whose handler fails is retried every
half minute, five times at most. One waits per order: an order placed
again while its message waits adds none.';

CREATE TABLE stock_low_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  product_id UUID NOT NULL,
  quantity BIGINT NOT NULL,
  threshold BIGINT NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE stock_low_queue IS 'Enqueued by shop-stock when a sale leaves a product low, its Inventory''s
available units at or below its reorderAt (Stock.low, D70), and by its
job OrderRestocks for one a count left low: shop-stock''s worker
ReorderStock files a restock request for the product. The message holds
the units available and the reorder level as the sale left them. One
waits per product: a later sale that leaves the product low replaces it,
so the worker reads the latest level, and the worker handles one
product''s message at a time. A message whose handler fails is retried
every half minute, five times at most.';

CREATE TABLE ticket_change_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  write TEXT NOT NULL,
  at BIGINT NOT NULL,
  before JSONB,
  "after" JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE ticket_change_queue IS 'The writes of Ticket''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

-- Add foreign key constraints

ALTER TABLE checkout
  ADD CONSTRAINT fk_checkout_customer_id
  FOREIGN KEY (customer_id)
  REFERENCES "user"(id)
  ON DELETE RESTRICT;

ALTER TABLE checkout
  ADD CONSTRAINT fk_checkout_counter_id
  FOREIGN KEY (counter_id)
  REFERENCES collection_point(id)
  ON DELETE RESTRICT;

ALTER TABLE checkout_holds_items
  ADD CONSTRAINT fk_checkout_holds_items_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE checkout_payments_actions
  ADD CONSTRAINT fk_checkout_payments_actions_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE checkout_payments_attempts
  ADD CONSTRAINT fk_checkout_payments_attempts_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE checkout_payments_issues
  ADD CONSTRAINT fk_checkout_payments_issues_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE checkout_payments_refunds
  ADD CONSTRAINT fk_checkout_payments_refunds_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE checkout_timers_deadlines
  ADD CONSTRAINT fk_checkout_timers_deadlines_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE CASCADE;

ALTER TABLE collection_point_calendar_bookings
  ADD CONSTRAINT fk_collection_point_calendar_bookings_collection_point_id
  FOREIGN KEY (collection_point_id)
  REFERENCES collection_point(id)
  ON DELETE CASCADE;

ALTER TABLE collection_point_calendar_closures
  ADD CONSTRAINT fk_collection_point_calendar_closures_collection_point_id
  FOREIGN KEY (collection_point_id)
  REFERENCES collection_point(id)
  ON DELETE CASCADE;

ALTER TABLE collection_point_calendar_settings
  ADD CONSTRAINT fk_collection_point_calendar_settings_collection_point_id
  FOREIGN KEY (collection_point_id)
  REFERENCES collection_point(id)
  ON DELETE CASCADE;

ALTER TABLE inventory_stock_holds
  ADD CONSTRAINT fk_inventory_stock_holds_inventory_id
  FOREIGN KEY (inventory_id)
  REFERENCES inventory(id)
  ON DELETE CASCADE;

ALTER TABLE inventory_stock_movements
  ADD CONSTRAINT fk_inventory_stock_movements_inventory_id
  FOREIGN KEY (inventory_id)
  REFERENCES inventory(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_client_id
  FOREIGN KEY (client_id)
  REFERENCES oauth_client(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_grant_id
  FOREIGN KEY (grant_id)
  REFERENCES oauth_grant(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_grant
  ADD CONSTRAINT fk_oauth_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_grant
  ADD CONSTRAINT fk_oauth_grant_client_id
  FOREIGN KEY (client_id)
  REFERENCES oauth_client(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_token
  ADD CONSTRAINT fk_oauth_token_grant_id
  FOREIGN KEY (grant_id)
  REFERENCES oauth_grant(id)
  ON DELETE CASCADE;

ALTER TABLE "order"
  ADD CONSTRAINT fk_order_customer_id
  FOREIGN KEY (customer_id)
  REFERENCES "user"(id)
  ON DELETE RESTRICT;

ALTER TABLE order_line
  ADD CONSTRAINT fk_order_line_order_id
  FOREIGN KEY (order_id)
  REFERENCES "order"(id)
  ON DELETE CASCADE;

ALTER TABLE order_line
  ADD CONSTRAINT fk_order_line_product_id
  FOREIGN KEY (product_id)
  REFERENCES product(id)
  ON DELETE RESTRICT;

ALTER TABLE refund
  ADD CONSTRAINT fk_refund_checkout_id
  FOREIGN KEY (checkout_id)
  REFERENCES checkout(id)
  ON DELETE RESTRICT;

ALTER TABLE refund_approvals_sign_offs
  ADD CONSTRAINT fk_refund_approvals_sign_offs_refund_id
  FOREIGN KEY (refund_id)
  REFERENCES refund(id)
  ON DELETE CASCADE;

ALTER TABLE refund_payments_actions
  ADD CONSTRAINT fk_refund_payments_actions_refund_row_id
  FOREIGN KEY (refund_row_id)
  REFERENCES refund(id)
  ON DELETE CASCADE;

ALTER TABLE refund_payments_attempts
  ADD CONSTRAINT fk_refund_payments_attempts_refund_id
  FOREIGN KEY (refund_id)
  REFERENCES refund(id)
  ON DELETE CASCADE;

ALTER TABLE refund_payments_issues
  ADD CONSTRAINT fk_refund_payments_issues_refund_row_id
  FOREIGN KEY (refund_row_id)
  REFERENCES refund(id)
  ON DELETE CASCADE;

ALTER TABLE refund_payments_refunds
  ADD CONSTRAINT fk_refund_payments_refunds_refund_row_id
  FOREIGN KEY (refund_row_id)
  REFERENCES refund(id)
  ON DELETE CASCADE;

ALTER TABLE restock_request
  ADD CONSTRAINT fk_restock_request_product_id
  FOREIGN KEY (product_id)
  REFERENCES product(id)
  ON DELETE CASCADE;

ALTER TABLE review
  ADD CONSTRAINT fk_review_product_id
  FOREIGN KEY (product_id)
  REFERENCES product(id)
  ON DELETE CASCADE;

ALTER TABLE review
  ADD CONSTRAINT fk_review_author_id
  FOREIGN KEY (author_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE "session"
  ADD CONSTRAINT fk_session_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE ticket
  ADD CONSTRAINT fk_ticket_customer_id
  FOREIGN KEY (customer_id)
  REFERENCES "user"(id)
  ON DELETE RESTRICT;

ALTER TABLE ticket_derived_derivations
  ADD CONSTRAINT fk_ticket_derived_derivations_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_provenance_records
  ADD CONSTRAINT fk_ticket_provenance_records_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE user_api_key
  ADD CONSTRAINT fk_user_api_key_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_credential
  ADD CONSTRAINT fk_user_credential_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_role_id
  FOREIGN KEY (role_id)
  REFERENCES role(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_checkout_holds_items_checkout_key ON checkout_holds_items USING BTREE (checkout_id, "key");

CREATE UNIQUE INDEX idx_checkout_payments_actions_checkout_action ON checkout_payments_actions USING BTREE (checkout_id, "action");

CREATE UNIQUE INDEX idx_checkout_payments_attempts_checkout_attempt ON checkout_payments_attempts USING BTREE (checkout_id, attempt);

CREATE UNIQUE INDEX idx_checkout_payments_issues_checkout_refund ON checkout_payments_issues USING BTREE (checkout_id, refund);

CREATE UNIQUE INDEX idx_checkout_payments_refunds_checkout_refund ON checkout_payments_refunds USING BTREE (checkout_id, refund);

CREATE UNIQUE INDEX idx_checkout_timers_deadlines_checkout_name ON checkout_timers_deadlines USING BTREE (checkout_id, "name");

CREATE UNIQUE INDEX idx_collection_point_calendar_bookings_collection_point_record ON collection_point_calendar_bookings USING BTREE (collection_point_id, record);

CREATE UNIQUE INDEX idx_collection_point_calendar_closures_collection_point_closure ON collection_point_calendar_closures USING BTREE (collection_point_id, closure);

CREATE UNIQUE INDEX idx_collection_point_calendar_settings_collection_point_name ON collection_point_calendar_settings USING BTREE (collection_point_id, "name");

CREATE UNIQUE INDEX idx_inventory_stock_holds_inventory_record ON inventory_stock_holds USING BTREE (inventory_id, record);

CREATE UNIQUE INDEX idx_inventory_stock_movements_inventory_seq ON inventory_stock_movements USING BTREE (inventory_id, seq);

CREATE INDEX idx_oauth_grant_user ON oauth_grant USING BTREE (user_id);

CREATE INDEX idx_oauth_token_grant ON oauth_token USING BTREE (grant_id);

CREATE INDEX idx_order_customer_placed_at ON "order" USING BTREE (customer_id, placed_at);

CREATE INDEX idx_product_search_trgm ON product USING GIN (search_text gin_trgm_ops);

CREATE UNIQUE INDEX idx_refund_approvals_sign_offs_refund_number ON refund_approvals_sign_offs USING BTREE (refund_id, number);

CREATE UNIQUE INDEX idx_refund_payments_actions_refund_row_action ON refund_payments_actions USING BTREE (refund_row_id, "action");

CREATE UNIQUE INDEX idx_refund_payments_attempts_refund_attempt ON refund_payments_attempts USING BTREE (refund_id, attempt);

CREATE UNIQUE INDEX idx_refund_payments_issues_refund_row_refund ON refund_payments_issues USING BTREE (refund_row_id, refund);

CREATE UNIQUE INDEX idx_refund_payments_refunds_refund_row_refund ON refund_payments_refunds USING BTREE (refund_row_id, refund);

CREATE UNIQUE INDEX uq_restock_request_one_open_per_product ON restock_request USING BTREE (open_product_id);

CREATE UNIQUE INDEX uq_review_one_per_author ON review USING BTREE (product_id, author_id) WHERE deleted_at IS NULL;

CREATE INDEX idx_review_search_trgm ON review USING GIN (search_text gin_trgm_ops);

CREATE INDEX idx_session_user ON "session" USING BTREE (user_id);

CREATE UNIQUE INDEX idx_ticket_derived_derivations_ticket_field ON ticket_derived_derivations USING BTREE (ticket_id, field);

CREATE UNIQUE INDEX idx_ticket_provenance_records_ticket_field ON ticket_provenance_records USING BTREE (ticket_id, field);

CREATE INDEX idx_user_api_key_user ON user_api_key USING BTREE (user_id);

CREATE UNIQUE INDEX uq_user_role_grant_user_role ON user_role_grant USING BTREE (user_id, role_id);

CREATE INDEX idx_checkout_change_queue_due_at ON checkout_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_checkout_change_queue_claim_expires_at ON checkout_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_checkout_change_queue_updated_at ON checkout_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_checkout_change_queue_ready_key ON checkout_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_checkout_change_queue_claimed_key ON checkout_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_checkout_deadline_queue_due_at ON checkout_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_checkout_deadline_queue_claim_expires_at ON checkout_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_checkout_deadline_queue_updated_at ON checkout_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_checkout_deadline_queue_row_key_use_id_name ON checkout_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_checkout_deadline_queue_row_key ON checkout_deadline_queue USING BTREE (row_key);

CREATE INDEX idx_order_placed_queue_due_at ON order_placed_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_order_placed_queue_claim_expires_at ON order_placed_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_order_placed_queue_updated_at ON order_placed_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX uq_order_placed_queue_ready_key ON order_placed_queue USING BTREE (order_id) WHERE state = 'ready';

CREATE INDEX idx_stock_low_queue_due_at ON stock_low_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_stock_low_queue_claim_expires_at ON stock_low_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_stock_low_queue_updated_at ON stock_low_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX uq_stock_low_queue_ready_key ON stock_low_queue USING BTREE (product_id) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_stock_low_queue_claimed_key ON stock_low_queue USING BTREE (product_id) WHERE state = 'claimed';

CREATE INDEX idx_ticket_change_queue_due_at ON ticket_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ticket_change_queue_claim_expires_at ON ticket_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ticket_change_queue_updated_at ON ticket_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_ticket_change_queue_ready_key ON ticket_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_ticket_change_queue_claimed_key ON ticket_change_queue USING BTREE (row_key) WHERE state = 'claimed';

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION checkout_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_checkout_bump_version
  BEFORE UPDATE ON checkout
  FOR EACH ROW EXECUTE FUNCTION checkout_bump_version();

CREATE OR REPLACE FUNCTION checkout_number_counts_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_checkout_number_counts_bump_version
  BEFORE UPDATE ON checkout_number_counts
  FOR EACH ROW EXECUTE FUNCTION checkout_number_counts_bump_version();

CREATE OR REPLACE FUNCTION checkout_payments_events_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_checkout_payments_events_bump_version
  BEFORE UPDATE ON checkout_payments_events
  FOR EACH ROW EXECUTE FUNCTION checkout_payments_events_bump_version();

CREATE OR REPLACE FUNCTION collection_point_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_collection_point_bump_version
  BEFORE UPDATE ON collection_point
  FOR EACH ROW EXECUTE FUNCTION collection_point_bump_version();

CREATE OR REPLACE FUNCTION inventory_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_inventory_bump_version
  BEFORE UPDATE ON inventory
  FOR EACH ROW EXECUTE FUNCTION inventory_bump_version();

CREATE OR REPLACE FUNCTION refund_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_refund_bump_version
  BEFORE UPDATE ON refund
  FOR EACH ROW EXECUTE FUNCTION refund_bump_version();

CREATE OR REPLACE FUNCTION refund_number_counts_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_refund_number_counts_bump_version
  BEFORE UPDATE ON refund_number_counts
  FOR EACH ROW EXECUTE FUNCTION refund_number_counts_bump_version();

CREATE OR REPLACE FUNCTION refund_payments_events_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_refund_payments_events_bump_version
  BEFORE UPDATE ON refund_payments_events
  FOR EACH ROW EXECUTE FUNCTION refund_payments_events_bump_version();

CREATE OR REPLACE FUNCTION ticket_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_bump_version
  BEFORE UPDATE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_bump_version();

-- Create the functions and triggers that enqueue a queue's message after a
-- write to a table's rows, whatever writes it

CREATE OR REPLACE FUNCTION order_enqueue_order_placed() RETURNS trigger AS $$
BEGIN
  -- Enqueues a message of OrderPlaced after a write to order (D80).
  IF TG_OP = 'UPDATE' AND NOT (OLD.status IS DISTINCT FROM NEW.status) THEN
    RETURN NULL;
  END IF;
  IF NEW.status = 'placed' THEN
    INSERT INTO order_placed_queue (order_id) VALUES (NEW.id)
      ON CONFLICT (order_id) WHERE state = 'ready' DO NOTHING;
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_order_enqueue_order_placed
  AFTER UPDATE ON "order"
  FOR EACH ROW EXECUTE FUNCTION order_enqueue_order_placed();

-- Create the functions and triggers that write a table's change queue: a
-- message for each write of a row its behaviors hear, whoever writes it

CREATE OR REPLACE FUNCTION "checkout_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of CheckoutChange for each write of a row of checkout
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."customer_id" IS DISTINCT FROM NEW."customer_id" OR OLD."lines" IS DISTINCT FROM NEW."lines" OR OLD."counter_id" IS DISTINCT FROM NEW."counter_id" OR OLD."pickup_at" IS DISTINCT FROM NEW."pickup_at" OR OLD."address" IS DISTINCT FROM NEW."address" OR OLD."holds_state" IS DISTINCT FROM NEW."holds_state" OR OLD."holds_total" IS DISTINCT FROM NEW."holds_total" OR OLD."payments_due" IS DISTINCT FROM NEW."payments_due" OR OLD."payments_authorized" IS DISTINCT FROM NEW."payments_authorized" OR OLD."payments_captured" IS DISTINCT FROM NEW."payments_captured" OR OLD."payments_refunded" IS DISTINCT FROM NEW."payments_refunded" OR OLD."payments_outstanding" IS DISTINCT FROM NEW."payments_outstanding" OR OLD."payments_overpaid" IS DISTINCT FROM NEW."payments_overpaid" OR OLD."payments_currency" IS DISTINCT FROM NEW."payments_currency" OR OLD."payments_state" IS DISTINCT FROM NEW."payments_state" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at" OR OLD."number_seq" IS DISTINCT FROM NEW."number_seq" OR OLD."number" IS DISTINCT FROM NEW."number") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "checkout_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('customer_id', NEW."customer_id", 'lines', NEW."lines", 'counter_id', NEW."counter_id", 'pickup_at', NEW."pickup_at", 'address', NEW."address", 'holds_state', NEW."holds_state", 'holds_total', NEW."holds_total", 'payments_due', NEW."payments_due", 'payments_authorized', NEW."payments_authorized", 'payments_captured', NEW."payments_captured", 'payments_refunded', NEW."payments_refunded", 'payments_outstanding', NEW."payments_outstanding", 'payments_overpaid', NEW."payments_overpaid", 'payments_currency', NEW."payments_currency", 'payments_state', NEW."payments_state", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at", 'number_seq', NEW."number_seq", 'number', NEW."number"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "checkout_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('customer_id', OLD."customer_id", 'lines', OLD."lines", 'counter_id', OLD."counter_id", 'pickup_at', OLD."pickup_at", 'address', OLD."address", 'holds_state', OLD."holds_state", 'holds_total', OLD."holds_total", 'payments_due', OLD."payments_due", 'payments_authorized', OLD."payments_authorized", 'payments_captured', OLD."payments_captured", 'payments_refunded', OLD."payments_refunded", 'payments_outstanding', OLD."payments_outstanding", 'payments_overpaid', OLD."payments_overpaid", 'payments_currency', OLD."payments_currency", 'payments_state', OLD."payments_state", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at", 'number_seq', OLD."number_seq", 'number', OLD."number"), jsonb_build_object('customer_id', NEW."customer_id", 'lines', NEW."lines", 'counter_id', NEW."counter_id", 'pickup_at', NEW."pickup_at", 'address', NEW."address", 'holds_state', NEW."holds_state", 'holds_total', NEW."holds_total", 'payments_due', NEW."payments_due", 'payments_authorized', NEW."payments_authorized", 'payments_captured', NEW."payments_captured", 'payments_refunded', NEW."payments_refunded", 'payments_outstanding', NEW."payments_outstanding", 'payments_overpaid', NEW."payments_overpaid", 'payments_currency', NEW."payments_currency", 'payments_state', NEW."payments_state", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at", 'number_seq', NEW."number_seq", 'number', NEW."number"), written, written);
  ELSE
    INSERT INTO "checkout_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('customer_id', OLD."customer_id", 'lines', OLD."lines", 'counter_id', OLD."counter_id", 'pickup_at', OLD."pickup_at", 'address', OLD."address", 'holds_state', OLD."holds_state", 'holds_total', OLD."holds_total", 'payments_due', OLD."payments_due", 'payments_authorized', OLD."payments_authorized", 'payments_captured', OLD."payments_captured", 'payments_refunded', OLD."payments_refunded", 'payments_outstanding', OLD."payments_outstanding", 'payments_overpaid', OLD."payments_overpaid", 'payments_currency', OLD."payments_currency", 'payments_state', OLD."payments_state", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at", 'number_seq', OLD."number_seq", 'number', OLD."number"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_checkout_changes
  AFTER INSERT OR UPDATE OR DELETE ON checkout
  FOR EACH ROW EXECUTE FUNCTION checkout_changes();

CREATE OR REPLACE FUNCTION "ticket_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of TicketChange for each write of a row of ticket
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."customer_id" IS DISTINCT FROM NEW."customer_id" OR OLD."subject" IS DISTINCT FROM NEW."subject" OR OLD."body" IS DISTINCT FROM NEW."body" OR OLD."order_id" IS DISTINCT FROM NEW."order_id" OR OLD."category" IS DISTINCT FROM NEW."category" OR OLD."priority" IS DISTINCT FROM NEW."priority" OR OLD."summary" IS DISTINCT FROM NEW."summary" OR OLD."provenance_in_review" IS DISTINCT FROM NEW."provenance_in_review" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('customer_id', NEW."customer_id", 'subject', NEW."subject", 'body', NEW."body", 'order_id', NEW."order_id", 'category', NEW."category", 'priority', NEW."priority", 'summary', NEW."summary", 'provenance_in_review', NEW."provenance_in_review", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('customer_id', OLD."customer_id", 'subject', OLD."subject", 'body', OLD."body", 'order_id', OLD."order_id", 'category', OLD."category", 'priority', OLD."priority", 'summary', OLD."summary", 'provenance_in_review', OLD."provenance_in_review", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), jsonb_build_object('customer_id', NEW."customer_id", 'subject', NEW."subject", 'body', NEW."body", 'order_id', NEW."order_id", 'category', NEW."category", 'priority', NEW."priority", 'summary', NEW."summary", 'provenance_in_review', NEW."provenance_in_review", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSE
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('customer_id', OLD."customer_id", 'subject', OLD."subject", 'body', OLD."body", 'order_id', OLD."order_id", 'category', OLD."category", 'priority', OLD."priority", 'summary', OLD."summary", 'provenance_in_review', OLD."provenance_in_review", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_changes
  AFTER INSERT OR UPDATE OR DELETE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_changes();
