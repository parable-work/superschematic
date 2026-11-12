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
- Sorted lists (`SortBy`): the sort is a `*string` field of the method's query parameters, a field or `-` and a field; a cursor holds the sort it was returned under, so `<Method>All` sends the same sort with every page, and the route refuses a sort it does not offer
- Filtered lists (`FilterBy`): the filter is a field of the method's query parameters, a struct with a member per field a caller may filter by, each a `*filterparse.Equals`, `Ordered` or `Text` of the HTTP runtime; the method sends it as `filter[field]=value` and `filter[field][op]=value`, and `<Method>All` sends it with every page

## Filters

A filtered method takes its filter in its query parameters. Each member
is a field's filter, nil for none: `Eq` and `In` on every field, and `Ne`
and `NotIn`, which keep the rows those two leave out, a row whose field
is null among them; `Gt`, `Gte`, `Lt` and `Lte` on a number, a date and a
time, `Contains` on text, and `IsNull` on a field that may be null. The
list keeps the rows every member holds for, and the route refuses a
field, an operator or a value it does not take. Before the request the
method refuses, with a `*namespaces.ValidationError`, what the route
would not read as it was given: an `In` or a `NotIn` of more than 100
values, or of a value that is blank or holds a comma or surrounding
space, a `Contains` of no text, and more than 50 filters. A cursor holds no filter, so ask for each next page
with the same one.

```go
bound := int64(10)
page, err := client.CatalogNamespace.ListParts(ctx, &namespaces.CatalogListPartsQueryParams{
	Filter: &namespaces.CatalogListPartsFilter{
		Name:     &filterparse.Text[string]{In: []string{"anvil"}},
		Quantity: &filterparse.Ordered[int64]{Gte: &bound},
	},
}, runtime.PageRequest{})
```
