# fixture-comments-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-comments-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-comments-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Review, ReviewCommentsConversations, ReviewCommentsEdits, ReviewCommentsMessages, ReviewCommentsReactions, Ticket, TicketCommentsConversations, TicketCommentsEdits, TicketCommentsMessages, TicketCommentsReactions,  } from '@schemas/fixture-comments-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-comments-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-comments-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-comments-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-comments-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-comments-db-types/validators/scalars/<name>` for granular imports
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







### Types (10)


- **Review** - A review's conversation of refs and code blocks alone, its bodies at
most 2000 code points, pinned and resolved by the permissions it names.

- **ReviewCommentsConversations** - On a table, one row per row that has had a message, kept for the table as a whole: the greatest message id, reaction number and stand the row's conversation has given. Every write of a conversation moves its row's version, which holds two writes of one conversation apart; a delete of the row leaves it, so a row re-created with the key numbers on from it and never reuses an id.

- **ReviewCommentsEdits** - On a table, one row per edit of a message, keyed by the message and the edit's number: what the message held before the edit, who made it and when. A delete or a purge of the message deletes them, and so does a delete of the row.

- **ReviewCommentsMessages** - On a table, one row per message of the row's conversation, keyed by its id: its thread and the message it quotes, its author, its state, its body, blocks and mentions, when it was posted and last edited, how many edits it has had, whether it is pinned, the clientId it was posted with, and for a top-level message who resolved its thread and when. A deleted message keeps its row; a purged one loses it. A delete of the row deletes them.

- **ReviewCommentsReactions** - On a table, one row per reaction a subject holds on a message, keyed by its number on the row, which orders reactions: the message, the emoji, the subject and when. unreact deletes the caller's; a delete or a purge of the message deletes them, and so does a delete of the row.

- **Ticket** - A ticket's conversation (D77, D103's step 4): messages and their
replies, edits within 15 minutes of a post, reactions, pins and resolved
threads, each write beside the row, and a caller holding tickets.moderate
edits and deletes another's message and purges.

- **TicketCommentsConversations** - On a table, one row per row that has had a message, kept for the table as a whole: the greatest message id, reaction number and stand the row's conversation has given. Every write of a conversation moves its row's version, which holds two writes of one conversation apart; a delete of the row leaves it, so a row re-created with the key numbers on from it and never reuses an id.

- **TicketCommentsEdits** - On a table, one row per edit of a message, keyed by the message and the edit's number: what the message held before the edit, who made it and when. A delete or a purge of the message deletes them, and so does a delete of the row.

- **TicketCommentsMessages** - On a table, one row per message of the row's conversation, keyed by its id: its thread and the message it quotes, its author, its state, its body, blocks and mentions, when it was posted and last edited, how many edits it has had, whether it is pinned, the clientId it was posted with, and for a top-level message who resolved its thread and when. A deleted message keeps its row; a purged one loses it. A delete of the row deletes them.

- **TicketCommentsReactions** - On a table, one row per reaction a subject holds on a message, keyed by its number on the row, which orders reactions: the message, the emoji, the subject and when. unreact deletes the caller's; a delete or a purge of the message deletes them, and so does a delete of the row.



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

Run from project root. Output: `types/typescript/fixture-comments-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
