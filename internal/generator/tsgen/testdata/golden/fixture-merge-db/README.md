# fixture-merge-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-merge-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-merge-db schema on the client side. It includes:

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
import type { GenericInt64, IdentityUUID, TemporalDateTime, Address, Board, Customer, CustomerProfile, Document, DocumentLabel, Label, LabelCode, LabelNote, LabelPair, LabelSlot, Purchase, Subscription,  } from '@schemas/fixture-merge-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-merge-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-merge-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-merge-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-merge-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-merge-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (3)


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.







### Types (13)


- **Address** - An address of a customer, which declares no relation back: the build
adds its customer_id from Customer's addresses.

- **Board** - A board of labels, through a join table the build adds (board_label).

- **Customer** - A customer, merged when two records are one person. Its status is a
Workflow, so its rows are deleted through the behaviors' core, and a
delete removes the row.

- **CustomerProfile** - One profile per customer: a merge keeps the profile of the customer that
stays.

- **Document** - A document labels go on.

- **DocumentLabel** - A label on a document, once per document: a merge of two labels a
document carries deletes the one that pointed at the label merged away.

- **Label** - A label people put on documents. Two labels that turn out to be one are
merged (D147): everything that pointed at one points at the other. A
label may sit under another. A delete sets deletedAt.

- **LabelCode** - A label's code in another system, once per label without regard to case:
its unique index is over an expression that reads the relation's column.

- **LabelNote** - A note about a label, which says who changed it last and when: a merge
stamps both on the notes it repoints. Its revision is a fence over the
label and the text, which the merge's repoint advances.

- **LabelPair** - Two labels that go together, once per pair: two relations of one table
to the merged table, and a key that holds both.

- **LabelSlot** - A label's place on a shelf, once per slot while it is open, and once
with no slot: its unique index is partial and holds nulls alike.

- **Purchase** - An order of a customer.

- **Subscription** - A person following a label, once while the row is live. A delete sets
deletedAt, so a merge that finds a person following both labels stamps
the one that pointed at the label merged away.



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

Run from project root. Output: `types/typescript/fixture-merge-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
