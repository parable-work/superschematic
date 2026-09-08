# psgen HTTP Runtime

This module defines the shared HTTP runtime owned by `psgen`, separate from generated API packages and separate from `schema-runtime`.

## Module Boundary

- Directory: `utils/psgen/http-runtime/go`
- Module path: `github.com/parable-work/superschematic/runtime/http/go`
- Ownership: generator-owned runtime for duplicated HTTP server scaffolding emitted by `internal/apigen`
- Non-goal: schema parsing, validation IR, or generated ORM/type-specific business wiring

## Package Shape

The runtime should grow as a small set of focused packages instead of a single grab-bag package:

- `response`: JSON responders and encode-safe write helpers
- `apperror`: shared app error model, validation error wrapper, and HTTP error responder
- `requestctx`: generic request context helpers that do not depend on generated API or ORM types
- `middleware`: reusable request middleware primitives with schema-agnostic dependencies
- `routing`: shared route-registration and handler-adapter scaffolding

The generated API package remains responsible for:

- `Config` and `Implementations`
- final route tables and endpoint-specific decode or validate wiring
- auth, tenant, and permission loaders that depend on generated ORM and API type imports
- encrypted payload and multipart adapters that depend on schema feature flags or generated file upload types

## First Extraction Wave

Move only the helpers that are already duplicated with the same behavior across generated APIs:

### From `response.tmpl`

- `RespondJSON`
- `RespondError`
- `RespondValidationErrors`
- `RespondCreated`
- `RespondNoContent`

### From `errors.tmpl`

- error code constants
- `AppError`
- `ValidationAppError`
- `RespondAppError`
- constructor helpers such as `NotFoundError`, `UnauthorizedError`, and `UniqueConstraintError`

### From `context.tmpl`

- `CheckContext`

### From `middleware.tmpl` only when type-agnostic

- `GetClientIP`
- request logger context storage and retrieval
- client IP context storage and retrieval

The runtime version of validation-aware helpers should depend on `github.com/parable-work/superschematic/runtime/schema/go/validate.ValidationErrors` instead of generated API type packages so the same implementation works for every generated schema.

## Deferred To Later Waves

Do not move these into the first extraction:

- `ContextWithDatabase` and `GetDatabase`
- user, session, tenant, and permission context helpers that currently store generated API scalar types
- `AuthMiddleware`
- `TenantCache`, `RolePermissionCache`, `TenantResolutionMiddleware`, and `TenantPermissionsMiddleware`
- encrypted payload middleware
- multipart and file upload helpers
- endpoint handler factories and per-route decode or validate logic

Those pieces either depend on generated ORM or API types today or still need an adapter seam before they can be shared cleanly.

## Generated Package Compatibility

The generated API package should keep its current public surface during the migration by emitting thin compatibility shims:

- type aliases for shared error types and constants
- wrapper functions that forward to runtime `response`, `apperror`, and `requestctx` packages
- minimal local helpers only where generated type aliases are still required

This keeps service code importing the generated API package while letting the implementation move under the hood into a single psgen-owned runtime.
