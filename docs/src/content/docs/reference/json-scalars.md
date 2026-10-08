---
title: JSON-valued scalars
description: The scalars whose value is not a string (Generic.JSON, Generic.StringMap, Geo.Location, Embedding.Vector), what each target types them as, how they are validated, and what a scalar of your own that holds JSON declares.
sidebar:
  order: 6
---

Most scalars hold a string or a number. Four in the core catalog hold a
JSON value instead. superscalar's metadata gives all four the `String`
primitive; their `json_schema` type mapping says what they hold, and every
validator keys off that mapping, not the scalar's name
([D14, amended](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-amended-a-json-object-or-array-scalar-holds-that-object-or-array)).

| Scalar | `json_schema` | Value on the wire |
|--------|---------------|-------------------|
| `Generic.JSON` | `any` | Any JSON value; null only when optional |
| `Generic.StringMap` | `object` | A JSON object of string values, such as `{"region": "eu"}` |
| `Geo.Location` | `object` | A point, a JSON object of two numbers in degrees: `{"lat": 37.7749, "lon": -122.4194}` ([below](#geolocation)) |
| `Embedding.Vector` | `array` | A JSON array of numbers, such as `[0.12, -0.5]` |

## A scalar of your own that holds JSON

A scalar whose language primitive is `object` says what JSON it holds
through its `json_schema` type mapping: `object`, `array` or `any`. The
validators take the mapping as the only sign that a scalar holds JSON, and
a pattern or a length, which are rules on a string, cancels `object` or
`array`. Without one of them, the schema runtimes and the engine would
check the scalar's values as strings, while the generated Go, TypeScript,
Python and Rust types hold an object
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

| Target | `Generic.StringMap` | `Geo.Location` | `Embedding.Vector` |
|--------|---------------------|----------------|--------------------|
| Go types | `map[string]string` | superscalar's `GeoLocation`, a struct whose `Lat` and `Lon` are tagged `json:"lat"` and `json:"lon"` | `[]float32` |
| TypeScript types | `Record<string, string>` | `{ lat: number; lon: number }` | `number[]` |
| Python types | `Dict[str, str]` | superscalar's `GeoLocation` `TypedDict`, or `Dict[str, Any]` where superscalar is not installed | `list[float]` |
| Rust types | `HashMap<String, String>` | `superscalar::metadata::geo_location::Location`, under the scalar crate's name | `Vec<f32>` |
| OpenAPI | `object` with string values | `object` | `array` of numbers |
| SQL | `JSONB` | `POINT` | `TEXT` |

The generated Go `Parse<Symbol>` reads the value's JSON text:
`ParseEmbeddingVector("[0.5, 1]")` returns `[]float32{0.5, 1}`, and
`ParseGeoLocation("{\"lon\": -122.4194, \"lat\": 37.7749}")` returns
`GeoLocation{Lat: 37.7749, Lon: -122.4194}`.

## Validation

For `Generic.StringMap`, `Geo.Location` and `Embedding.Vector`, every
validator (the Go, TypeScript and Python schema runtimes and the generated
Go, TypeScript, Python and Rust validators) follows one rule:

- The JSON object (for `Generic.StringMap` and `Geo.Location`) or JSON
  array (for `Embedding.Vector`) is a value. So is an empty one, where the
  scalar allows it.
- A string holding the value's JSON text, such as `"{\"region\": \"eu\"}"`,
  is accepted on input. The runtimes' parse step reads it into the object
  or array. An empty string is no value.
- Any other JSON type is one `type` error at the field's path: a number, a
  boolean, an array for `Generic.StringMap` or `Geo.Location`, an object
  for `Embedding.Vector`.
- A null or missing required value is `required`, a null optional one is
  absent, and a null list element is `required` at its index.
- What the value holds is superscalar's check: a `Generic.StringMap` value
  must be a string, an `Embedding.Vector` element a number, and a
  `Geo.Location` has exactly `lat` and `lon`, each a number in range. Each
  validator names that failure as it names any failure only the scalar
  core finds
  ([D14](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-a-failing-scalar-value-is-one-error-named-by-the-rule-it-breaks)):
  the generated Go, TypeScript and Rust validators and the TypeScript
  runtime use the core's name (`parse`, `custom`, `range`), the Go runtime
  says `pattern`, the Python runtime `custom`, and the generated Python
  validator `invalid`, once at the field for a list. The parity matrix
  holds `Geo.Location`'s failures under each validator's name.

The typed decoders take only the object or array: the generated Go types,
the Go API routes and the TypeScript API server answer the JSON text with
`type`, or fail to decode it. The generated Rust validators pass the JSON
text, and `parse_<type>` then fails to decode it, since serde reads the
field as a map, a struct or a vector. The generated Python types read the
JSON text into the value, and so does the Python SDK, which types a body
argument of such a scalar as the types package's alias and sends the
object or array the text holds.

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

`Geo.Location` is a point: a JSON object of two numbers in decimal
degrees, `{"lat": 37.7749, "lon": -122.4194}`, with `lat` from -90 to 90
and `lon` from -180 to 180, each bound included, and no other key.
superscalar refuses the `"lat,lon"` string, an unknown, missing or
duplicate key, a member that is not a number and a degree out of range,
and every validator hands it the value. Its
canonical text writes each number as `JSON.stringify` does:
`{"lat": 90.0, "lon": -180}` reads back as `{"lat":90,"lon":-180}`
([D14, amended](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d14-amended-geolocation-is-a-lat-lon-object)).

`{"lat": 0, "lon": 0}` is a location, not a missing one. The generated Go
types tell it apart from an absent or null value: a type's `UnmarshalJSON`
notes a required `Geo.Location`, or a value of a required map of them,
that the JSON left absent or null, which `Validate` reports as `required`.
It also checks each value's own JSON with superscalar, so an unknown or
missing key, which `encoding/json` would drop or zero-fill, is refused at
its path as `custom`; a `@strictJSON` type's decoder refuses an unknown
key outright. A value set after decoding is checked as set.

A Go API route decodes a body argument on its own, with
`runtime/http/go/bodyargs`, and checks a `Geo.Location` the same way:
each value, alone, in a list or in a map, goes to superscalar as its JSON
text before the route decodes it. An unknown, duplicate or missing key, a
member that is not a number and a degree out of range are refused at the
value's path under the core's name (`custom`, `range`, `parse`), and
`{"lat": 0, "lon": 0}` reaches the implementation as a location. The
route passes the check in, as
`bodyargs.CheckJSON(scalars.ValidatorFor("Geo.Location"))`, so the HTTP
runtime does not import superscalar itself. A `Generic.StringMap` body
argument is checked the same way.

In Postgres a `Geo.Location` column is a `POINT`, which is `(x, y)`: x is
the longitude and y the latitude, so `{"lat": 37.7749, "lon": -122.4194}`
is stored as `point(-122.4194, 37.7749)`. The generated Go ORM writes and
reads it that way, its versioned history included, and the column needs no
PostGIS. The ORM does not yet read or write a list of locations
(`POINT[]`). A version graph cannot hold one: no value class reads
`POINT`, so the graph generator and the engine's `Branches` refuse a
member field of `Geo.Location`.
