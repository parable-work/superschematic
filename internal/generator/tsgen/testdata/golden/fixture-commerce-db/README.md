# fixture-commerce-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-commerce-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-commerce-db schema on the client side. It includes:

- **Type definitions** - TypeScript interfaces for all types, scalars, enums, and unions
- **Validation** - Client-side validation matching server-side constraints

## Installation

```bash
bun install
bun run build
```

## Usage

### Importing Types

```typescript
// Type-only import (zero runtime) - use for smallest bundle
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Checkout, CheckoutHoldsItems, Inventory, InventoryStockHolds, InventoryStockMovements, Line,  } from '@schemas/fixture-commerce-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-commerce-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-commerce-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-commerce-db-types';

// Optional validation
const [valid, errors1] = validateEmail("user@example.com");

// Required validation
const [valid2, errors2] = validateEmailRequired(null);
```

### Validation Error Format

Validation errors follow a standardized format (see `validation_errors.md`):

```typescript
{
  "fieldName": [
    { "validator": "maxLength", "message": "must be less than 255 characters" }
  ],
  "fieldName2": [
    { "validator": "required", "message": "required field" }
  ],
  "nestedField": {
    "nestedFieldName": [
      { "validator": "pattern", "message": "invalid format" }
    ]
  }
}
```

## Generated Files

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-commerce-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-commerce-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (4)


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Generic.JSON** - Any valid JSON value: object, array, primitive, or null
  - TypeScript type: `JSONValue`


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.







### Types (6)


- **Checkout** - A checkout that holds its lines and its gift wrap while it waits for
payment (D70, D103's step 4): entering held takes every item or none,
through the contract Supply each Inventory implements; placed commits
them, expired and cancelled let them go, and returned puts the units
back.

- **CheckoutHoldsItems** - On a table, each item the claim has taken, keyed by its hold and index: the supply it names, what it asks, its state and the unit price the supply held it at, and the Workflow entry it was taken at. An item its fields ask that it has not taken is no row; the items field shows it pending.

- **Inventory** - What a shop sells (D70, D103's step 4): units on hand and units claims
hold, counted by Stock, each change a movement of its ledger. A caller
holding stock.adjust receives, adjusts and counts it; its price, in GBP
minor units, is what a line is held at; and it reads low at or below
its reorder level.

- **InventoryStockHolds** - On a table, the supply's record of each claim item it holds or held, keyed by the claim's type and key, the hold and the item's index: what the item asked, its state, which a call moves only forward, so a call made again changes nothing, and the price it was held at. A deleted claim's records retire under a key of their own: what it held is released, what it committed stays sold, and a claim created again with its key holds afresh.

- **InventoryStockMovements** - On a table, every change of what is on hand, append-only, keyed by its sequence from 1: its kind, its delta, what was on hand after it, its reason and reference, the claim and item it was for, who made it and when. onHand always equals the sum of the deltas.

- **Line** - One line of a checkout: the inventory it takes and how many.



## Development

### Building

```bash
bun run build
```

### Watching for Changes

```bash
bun run watch
```

### Cleaning

```bash
bun run clean
```

## Regeneration

```bash
superschematic build <service-dir>
```

Run from project root. Output: `types/typescript/fixture-commerce-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
