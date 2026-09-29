# fixture-version-graph-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-version-graph-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-version-graph-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, IdentityUserID, TemporalDate, TemporalDateTime, TemporalDuration, TemporalTime, RecipeEntityKind, RecipePatchOperation, Verdict, Cover, Ingredient, Note, Recipe, RecipeCommit, RecipePatch, RecipeRef, RecipeRelease, RecipeSnapshotEntry, Step, Tasting, Utensil,  } from '@schemas/fixture-version-graph-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-version-graph-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-version-graph-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-version-graph-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-version-graph-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-version-graph-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (8)


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Generic.JSON** - Any valid JSON value: object, array, primitive, or null
  - TypeScript type: `JSONValue`


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Identity.UserID** - UUID v4 string as base62
  - TypeScript type: `string`
  - Pattern: `^[0-9A-Za-z]{1,22}$`


- **Temporal.Date** - Calendar date, normalized to ISO 'YYYY-MM-DD'. Accepts ISO ('2025-01-01'), slash-separated ('2025/01/15', '01/15/2025'), named-month ('January 15, 2025', 'Jan 15, 2025'), and full RFC3339 datetime (the time portion is dropped).
  - TypeScript type: `string`
  - Max length: 40


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.


- **Temporal.Duration** - Duration for timeouts and intervals
  - TypeScript type: `string`
  - Pattern: `^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$`
  - Max length: 32


- **Temporal.Time** - Time of day. 24-hour 'HH:MM' or 'HH:MM:SS' (hours 00-23), or 12-hour 'H:MM'/'HH:MM' with optional ':SS' and required AM/PM suffix (hours 1-12). Seconds and the AM/PM separator space are optional.
  - TypeScript type: `string`
  - Pattern: `^(?:(?:[01][0-9]|2[0-3]):[0-5][0-9](?::[0-5][0-9])?|(?:0?[1-9]|1[0-2]):[0-5][0-9](?::[0-5][0-9])?\s?[AaPp][Mm])$`





### Enums (3)


- **RecipeEntityKind** - The entity kinds of the Recipe version graph.
  - Values: `Cover`, `Ingredient`, `Note`, `Step`, `Tasting`, `Utensil`

- **RecipePatchOperation** - What a patch of the Recipe version graph does to one entity.
  - Values: `Add`, `Update`, `Delete`

- **Verdict** - How a tasting went.
  - Values: `Again`, `Tweak`, `Never`




### Types (12)


- **Cover** - The recipe's cover photo: at most one per ref.

- **Ingredient** - An ingredient one step uses.

- **Note** - A cook's note, threaded under another note.

- **Recipe** - A recipe: the stable identity its steps and ingredients are versioned under.

- **RecipeCommit** - A commit of the Recipe version graph: the exact row versions one ref sealed.

- **RecipePatch** - One entity a commit of the Recipe version graph changed, pinned to the row version it sealed.

- **RecipeRef** - A line of the Recipe version graph: a primary line when parentRef is null, else a change set.

- **RecipeRelease** - The released commit of one root of the Recipe version graph; its history is the release log.

- **RecipeSnapshotEntry** - One entity of a snapshotted commit of the Recipe version graph, pinned to the row version its tree holds.

- **Step** - One step of a recipe, ordered by position; updatedBy names its row's writer.

- **Tasting** - A tasting of the recipe. Its columns hold a value of every class a
descriptor names.

- **Utensil** - A utensil the recipe needs, keyed by a plain UUID rather than an
AutoGenerate one.



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

Run from project root. Output: `types/typescript/fixture-version-graph-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
