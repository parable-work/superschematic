---
title: JSON-valued scalars
description: The scalars whose value is not a string (Generic.JSON, Generic.StringMap, Embedding.Vector), what each target types them as, how they are validated, and what a scalar of your own that holds JSON declares.
sidebar:
  order: 6
---

Most scalars hold a string or a number. Three in the core catalog hold a
JSON value instead, and one is not settled yet. superscalar's metadata
gives all four the `String` primitive; their `json_schema` type mapping
says what they hold, and every validator keys off that mapping, not the
scalar's name
([D14, amended](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-amended-a-json-object-or-array-scalar-holds-that-object-or-array)).

| Scalar | `json_schema` | Value on the wire |
|--------|---------------|-------------------|
| `Generic.JSON` | `any` | Any JSON value; null only when optional |
| `Generic.StringMap` | `object` | A JSON object of string values, such as `{"region": "eu"}` |
| `Embedding.Vector` | `array` | A JSON array of numbers, such as `[0.12, -0.5]` |
| `Geo.Location` | `object` | Not settled: see [below](#geolocation) |

## A scalar of your own that holds JSON

A scalar whose language primitive is `object` says what JSON it holds
through its `json_schema` type mapping: `object`, `array` or `any`. The
validators take the mapping as the only sign that a scalar holds JSON, and
a pattern or a length, which are rules on a string, cancels `object` or
`array`, as it does for `Geo.Location`. Without one of them, the schema
runtimes and the engine would check the scalar's values as strings, while
the generated Go, TypeScript, Python and Rust types hold an object
([D14, amended](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-amended-a-scalar-that-holds-json-says-which-json)).

```yaml
scalars:
  Acme.Settings:
    name: Acme.Settings
    description: A tenant's settings, as a JSON object
    languagePrimitive: object
    typeMappings:
      json_schema: object
```

A build refuses an `object` scalar the validators would check as a string,
and every `object` scalar that is a file upload, and so do the engine, when
a schema is defined or published, and `RegisterScalars`, for an
extension's catalog row. The error names the scalar and says what to
change:

- With no such mapping: add `typeMappings: { json_schema: object }`, or
  `array`, or `any` (a catalog row sets `JSONSchemaType`), use the
  catalog's `Generic.JSON` for free-form JSON, or model a value with known
  fields as a nested object type.
- With `object` or `array` and a pattern or a length: drop the pattern or
  the length.
- For a file-upload scalar, with a `json_schema` mapping or without: give
  it the string primitive. Its value is a file part, not JSON, and acme's
  `Acme.Photo` row has the `String` primitive too.

The loader checks a scalar after it fills it in from the catalog, so a
catalog scalar a schema names by name and `languagePrimitive: object`, as
`superschematic format --to=json` writes `Generic.JSON`, takes the
catalog's row, mapping included, and loads.

The engine knows only the builtin catalog, so to any other scalar a
document declares it applies only what the document writes.
`superschematic format --to=json` writes such a scalar's name, its
language primitive and, when the binary's catalog gives it `object`,
`array` or `any`, its `json_schema` mapping, so the engine holds an
extension's JSON scalar to JSON. It does not write the row's pattern,
lengths or other rules, and the engine checks none of them.

## What each target generates

| Target | `Generic.StringMap` | `Embedding.Vector` |
|--------|---------------------|--------------------|
| Go types | `map[string]string` | `[]float32` |
| TypeScript types | `Record<string, string>` | `number[]` |
| Python types | `Dict[str, str]` | `list[float]` |
| Rust types | `HashMap<String, String>` | `Vec<f32>` |
| OpenAPI | `object` with string values | `array` of numbers |
| SQL | `JSONB` | `TEXT` |

The generated Go `Parse<Symbol>` reads the value's JSON text:
`ParseEmbeddingVector("[0.5, 1]")` returns `[]float32{0.5, 1}`.

## Validation

For `Generic.StringMap` and `Embedding.Vector`, every validator (the Go,
TypeScript and Python schema runtimes and the generated Go, TypeScript and
Python validators) follows one rule:

- The JSON object (for `Generic.StringMap`) or JSON array (for
  `Embedding.Vector`) is a value. So is an empty one.
- A string holding the value's JSON text, such as `"{\"region\": \"eu\"}"`,
  is accepted on input. The runtimes' parse step reads it into the object
  or array. An empty string is no value.
- Any other JSON type is one `type` error at the field's path: a number, a
  boolean, an array for `Generic.StringMap`, an object for
  `Embedding.Vector`.
- A null or missing required value is `required`, a null optional one is
  absent, and a null list element is `required` at its index.
- What the value holds is superscalar's check: a `Generic.StringMap` value
  must be a string, an `Embedding.Vector` element a number. Each validator
  names that failure as it names any failure only the scalar core finds
  ([D14](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-a-failing-scalar-value-is-one-error-named-by-the-rule-it-breaks)),
  so the name differs between validators.

The typed decoders take only the object or array: the generated Go types,
the Go API routes and the TypeScript API server answer the JSON text with
`type`, or fail to decode it. The generated Python types read the JSON text
into the value, and so does the Python SDK, which types a body argument of
either scalar as the types package's alias and sends the object or array
the text holds.

`Generic.JSON` takes any JSON value but null: an object, an array, a
string, a number or a boolean, with no type check. A null or missing
required one is `required`, and a null list element is `required` at its
index.

## Null in an optional Generic.JSON

Null is a value of an optional single `Generic.JSON` field or argument. It
means "set to nothing", and it stays apart from an absent key, which means
"not present". A service can then clear a stored value through it: the
client sends `null` to clear it and leaves the key out to keep it
([D14, amended](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-amended-an-optional-genericjson-takes-null-as-a-value)).
Every validator accepts both, so the difference is in what the decoders
hand over:

| Target | Absent | Null |
|--------|--------|------|
| Go API routes, a body argument | `GenericJSON(nil)` | `GenericJSON("null")` |
| Go types, an input type's field | `InputField` not set | `IsNull()` |
| Go types, any other type's field | `nil` | a pointer to `GenericJSON("null")` |
| TypeScript API server and types | `undefined` | `null` |
| Rust API server, a body argument | `None` | `Some(Value::Null)` |
| Python types | not in `model_fields_set` | `None`, in `model_fields_set` |
| Rust types | `None` | `Some(Value::Null)` |

Each SDK sends the two apart. The Go SDK leaves out a nil
`*types.GenericJSON` and sends a pointer to `GenericJSON("null")` as null.
The TypeScript SDK leaves out `undefined` and sends `null`. The Rust SDK
leaves out `None` and sends `Some(Value::Null)`. The Python SDK leaves out
an optional `Generic.JSON` argument the caller does not pass (its default
is `UNSET`) and sends `None` as null; it sends an input type's field as
null when the model was given `None` for it.

The rule covers a single value. An optional `Generic.JSON[]`, list of
lists or map that is null is absent, as any other list or map is. A
`Generic.JSON` in the query string, a `GET` argument or a
`QueryParam<T>`, has no null to carry.

## Geo.Location

`Geo.Location`'s metadata contradicts itself: its `json_schema` is `object`,
its SQL type `POINT` and its TypeScript type `{ lat: number; lon: number }`,
but it also has the pattern `^-?\d+(\.\d+)?,-?\d+(\.\d+)?$` and the example
`37.7749,-122.4194`. The generated types do not agree on its wire form:

- The Go type is a struct with no JSON tags, so it writes
  `{"Lat": 37.7749, "Lon": -122.4194}` and refuses `"37.7749,-122.4194"`.
  The generated Go `Validate` tests the pattern on the struct and does not
  build.
- The generated TypeScript, Python and Rust validators test the pattern, so they
  accept `"37.7749,-122.4194"` and refuse `{"lat": 37.7749, "lon": -122.4194}`.
- The schema runtimes check it as a string with that pattern.

Until superscalar's metadata agrees with itself, every validator checks it
as a string, and a schema that needs Go types should not use it.
