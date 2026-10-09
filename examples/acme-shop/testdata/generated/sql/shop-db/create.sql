-- Generated PostgreSQL DDL for schema: shop-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;


-- Create tables (without foreign key constraints)

CREATE TABLE "order" (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  customer_id UUID NOT NULL,
  status TEXT NOT NULL,
  placed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  shipping_address TEXT NOT NULL,
  cancel_reason TEXT
);

COMMENT ON TABLE "order" IS 'A shopper''s order. Staff look orders up by customer, newest first.';

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
  in_stock BOOLEAN NOT NULL,
  UNIQUE (sku)
);

COMMENT ON TABLE product IS 'Something the shop sells.';

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
  deleted_by UUID
);

-- Add search_text generated column for trigram search
ALTER TABLE review ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE(title, '') || ' ' || COALESCE(body, '')
) STORED;

COMMENT ON TABLE review IS 'A shopper''s review of a product. Shoppers search reviews by their text,
and each shopper reviews a product once. A moderator hides a review by
deleting it; the row stays, marked deleted.';

CREATE TABLE "session" (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  jti UUID NOT NULL,
  user_id UUID NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  UNIQUE (jti)
);

COMMENT ON TABLE "session" IS 'A signed-in session. The session auth provider looks a bearer token up
by its jti.';

CREATE TABLE stock_level (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  product_id UUID NOT NULL,
  quantity BIGINT NOT NULL
);

COMMENT ON TABLE stock_level IS 'How many units of a product the warehouse holds.';

CREATE TABLE "user" (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  email CITEXT NOT NULL,
  "name" VARCHAR(80) NOT NULL,
  UNIQUE (email)
);

COMMENT ON TABLE "user" IS 'A person who can sign in to the shop.';

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

COMMENT ON TABLE order_placed_queue IS 'Placed with each order, in the transaction that writes it: shop-orders''
worker FulfilOrders handles each message, and a message whose handler
fails is retried every half minute, five times at most.';

-- Add foreign key constraints

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

ALTER TABLE stock_level
  ADD CONSTRAINT fk_stock_level_product_id
  FOREIGN KEY (product_id)
  REFERENCES product(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_order_customer_placed_at ON "order" USING BTREE (customer_id, placed_at);

CREATE UNIQUE INDEX uq_review_one_per_author ON review USING BTREE (product_id, author_id) WHERE deleted_at IS NULL;

CREATE INDEX idx_review_search_trgm ON review USING GIN (search_text gin_trgm_ops);

CREATE INDEX idx_order_placed_queue_due_at ON order_placed_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_order_placed_queue_claim_expires_at ON order_placed_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';
