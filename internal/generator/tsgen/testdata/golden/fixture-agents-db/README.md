# fixture-agents-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-agents-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-agents-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Account, AccountBudgetBuckets, AccountBudgetHolds, AccountBudgetMeters, Job, JobDeadline, JobLeaseDirectives, JobRetriesAttempts, Session, SessionDeadline, Workspace, WorkspaceBudgetBuckets, WorkspaceBudgetHolds, WorkspaceBudgetMeters,  } from '@schemas/fixture-agents-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-agents-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-agents-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-agents-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-agents-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-agents-db-types/validators/scalars/<name>` for granular imports
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







### Types (14)


- **Account** - An account an agent runs on (D104): forty questions an hour and a million
tokens over any day, each charged to its workspace too. The provider's own
count of its tokens, read every few minutes, replaces what the account
counted until the provider's period rolls over (observe, for callers who
hold accounts.observe). Usage that takes it past a cap moves it out, until
a person moves it back.

- **AccountBudgetBuckets** - A rolling meter's usage, a sixtieth of its period each, keyed by the meter and the bucket's start; a write deletes the ones that left the window.

- **AccountBudgetHolds** - What the row, a scope, holds of a meter for each row inside it, keyed by the meter, that row's type and its key.

- **AccountBudgetMeters** - One row per meter the row has written, keyed by the meter's name: what it has used in its current period, what it has reserved (its own reservation and what it holds for the rows inside it), the limit setLimit gave it, the start of the period its used counts from, its own reservation with the lease token it was made under, the token onExceeded last directed under, the scope that holds its reservation, and the usage a provider last reported, with its limit, when, and until when it holds.

- **Job** - A job a worker takes (D104). The worker leases it with a fencing token and
presents the token with every write while it holds it, so a process whose
lease lapsed cannot write after another took the job. A lease the worker
stops renewing expires when its deadline falls due and moves the job back
to queued; the third expiry fails it. A timeout waits a minute before the
job runs again, twice as long after each next one up to ten minutes, and
then moves it from waiting back to queued; a build failure is retried once
per revision; a job that has failed for half an hour gives up; and at most
four attempts start in an hour. Its revision is not named version, which a
table that composes behaviors keeps its own version in (_version).

- **JobDeadline** - The deadlines Job's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **JobLeaseDirectives** - One row per directive sent to the holder of the row's lease, keyed by its number in the lease: the token it was sent under, what it asks, its data, the dedupeKey it was sent with, who sent it and when, and when the holder acknowledged it. Every end of a lease deletes its directives, so none reaches the next holder.

- **JobRetriesAttempts** - One row per attempt the row recorded, keyed by its number, the order they were recorded in: when, its failure class (absent for a success), its score, whether its result was kept, its signature, its predicates and its detail, who recorded it, and the keyField value it counted under. The window, stuckAfter's streak, keepBest's neverRegress and a backoff's wait read it.

- **Session** - A session a device opens for an agent (D104). Only the device's principal
beats it, and it stays live three minutes after its last beat, and fifteen
while its agent is in a turn, counted from the later of the beat and the
turn's start. A session not beaten in time is missed: it moves to missing,
and the leases its device holds on jobs are expired, but for one the device
renewed since its last beat. Its next beat brings it back to idle.

- **SessionDeadline** - The deadlines Session's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **Workspace** - A workspace whose accounts' usage rolls up into its own caps (D104): sixty
questions an hour, and two million tokens over any day.

- **WorkspaceBudgetBuckets** - A rolling meter's usage, a sixtieth of its period each, keyed by the meter and the bucket's start; a write deletes the ones that left the window.

- **WorkspaceBudgetHolds** - What the row, a scope, holds of a meter for each row inside it, keyed by the meter, that row's type and its key.

- **WorkspaceBudgetMeters** - One row per meter the row has written, keyed by the meter's name: what it has used in its current period, what it has reserved (its own reservation and what it holds for the rows inside it), the limit setLimit gave it, the start of the period its used counts from, its own reservation with the lease token it was made under, the token onExceeded last directed under, the scope that holds its reservation, and the usage a provider last reported, with its limit, when, and until when it holds.



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

Run from project root. Output: `types/typescript/fixture-agents-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
