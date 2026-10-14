# example.com/schemas/sdk/go/fixture-paged-api

Generated Go SDK for the `fixture-paged-api` API. Auto-generated from GraphQL schema by `superschematic`.

**Do not edit manually.** Regenerate via: `superschematic build schemas/fixture-paged-api/schema.json` (from the schemas root).

## Features

- Typed namespace clients and endpoint methods
- Auth header injection (static token or token provider callback)
- Timeout and base URL configuration
- Query/path/body request handling
- Encrypted payload helpers for encrypted endpoints
- Multipart file upload helpers
- Typed API errors by status code
- Paged lists (`@paginated`): a paged method takes a `runtime.PageRequest` and returns one `runtime.Page`, and its `<Method>All` yields every item as an `iter.Seq2`, asking for each page as the loop reaches it
