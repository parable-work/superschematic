-- Generated PostgreSQL DDL for schema: shop-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

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

-- Add foreign key constraints

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
