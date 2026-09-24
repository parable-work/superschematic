---
title: Arrays of arrays
description: Declare a list of lists (T[][]) in TypeScript, JSON or YAML schemas; what each generator writes for it, where it is accepted and refused, and how it is validated.
sidebar:
  order: 5
---

A field type can be a list of lists, `T[][]`: grid rows of cells, a
polygon as a list of points, batches of vectors. `T` is anything a `T[]`
takes: a scalar, an enum, an object type or a union. Nesting stops at two
levels. For anything deeper, declare a named object type for the inner
list.

## Declare one

In a TypeScript schema, every spelling of a list of lists loads the same
way:

```ts
import { Nullable, Validate } from "@superschematic/schema";
import { Point, Shade } from "./drawing.schema";

export abstract class Drawing {
  // One inner list per grid row.
  labels: string[][];
  shades: Array<Array<Shade>>;
  polygons: Array<Point[]>;
  columns: Array<string>[];
  frozen: ReadonlyArray<ReadonlyArray<string>>;

  // Optional: the whole field may be absent or null.
  samples: Nullable<number[][]>;

  // listMin and listMax bound the outer list; min applies to every number.
  weights: Validate<number[][], { listMin: 1; listMax: 64; min: 0 }>;
}
```

`readonly (readonly T[])[]` is accepted too. Converting a schema file to
TypeScript with [`superschematic format --to=ts`](/superschematic/reference/cli/)
writes `T[][]`.

In a JSON or YAML schema, the field's `typeRef` sets `isArrayOfArrays`
next to `isArray`:

```yaml
types:
  Drawing:
    name: Drawing
    role: EmbeddedStruct
    fields:
      - name: labels
        typeRef: { name: string, isArray: true, isArrayOfArrays: true }
        required: true
      - name: polygons
        typeRef: { name: Point, isArray: true, isArrayOfArrays: true }
```

`isArrayOfArrays` without `isArray`, or with `isMap`, fails the load. The
schema-file JSON Schema (`superschematic json-schema`) has the key.

## What each generator writes

| Target | `T[]` | `T[][]` |
| --- | --- | --- |
| Go types | `[]T` | `[][]T`. An optional field of an input type is `InputField[[][]T]`. |
| TypeScript types | `T[]` | `T[][]`, and `T[][] \| null` when optional |
| Python types (pydantic) | `List[T]` | `List[List[T]]`, and `Optional[List[List[T]]]` when optional |
| Rust types (serde) | `Vec<T>` | `Vec<Vec<T>>`, and `Option<Vec<Vec<T>>>` when optional |
| OpenAPI and MCP tool documents | `{"type": "array", "items": T}` | `{"type": "array", "items": {"type": "array", "items": T}}`. `minItems` and `maxItems` sit on the outer array; every other constraint sits on the innermost items. |
| Postgres column | native `T[]` for a scalar | `JSONB`, `NOT NULL` when required. Postgres multi-dimensional arrays must be rectangular, and lists of lists are often ragged. |
| Go ORM | native array scan and encode | the JSON codec that maps and `@jsonField` columns use. A nil inner list is stored as `[]`; a union element is decoded through the union's wrapper. |
| Go API routes | `[]T` | `[][]T` for a body argument and a response. A nil inner list of a response is sent as `[]`. |
| SDKs | `[]T`, `T[]`, `list[T]`, `Vec<T>` | `[][]T` (Go), `T[][]` (TypeScript), `list[list[T]]` (Python), `Vec<Vec<T>>` (Rust) |

A schema without a list of lists generates exactly what it did before the
feature existed, and its IR JSON is unchanged: `isArrayOfArrays` is omitted
when false.

The TypeScript API server (`api: { language: "TYPESCRIPT" }`) does not
render `T[][]` yet. It types a list-of-lists body argument or response as
`T[]`. A list-of-lists field of a request input type or a response type is
right, because the server takes those types, and the input type's
validator, from the TypeScript types package.

## Where it is accepted

- Fields of DB, API and General types. In a DB table, the element type of
  a list-of-lists column is anything but another table.
- Fields of request input types and response types.
- Operation arguments that travel in the request body: any argument of a
  `POST`, `PUT`, `PATCH` or `DELETE` operation that is not a path or query
  parameter. The SDK tool documents give it as a list-of-lists argument.
- Operation responses.

## Where it is refused

Each refusal fails the load with an error that names the field or
argument.

| Context | Error |
| --- | --- |
| A third level (`T[][][]`) | `arrays nest at most two levels (T[][]); declare a named object type for the inner list` |
| A map value (`Record<string, T[][]>`, or a list of maps of lists) | `map values cannot be arrays of arrays; declare a named object type for the value` |
| A nullable inner list (`Nullable<T[]>[]`, `(T[] \| null)[]`) | `the inner lists of an array of arrays cannot be null; write Nullable<T[][]> to make the field optional` |
| List bounds on the inner lists (`Validate<T[], { listMax: 3 }>[]`) | `list bounds of an array of arrays apply to the outer list; write Validate<T[][], { listMin, listMax }>` |
| A query parameter | `<Set>.<operation>: query parameter "<name>" cannot be an array of arrays` |
| A path parameter | `<Set>.<operation>: path parameter "<name>" cannot be an array of arrays` |
| An argument of a `GET` operation, or of an operation without a method | `<Set>.<operation>: argument "<name>" is an array of arrays, which only a request body carries; declare a POST, PUT, PATCH or DELETE method, or move it into an input type` |
| An env config field | `<Type>.<field>: env config fields cannot be arrays of arrays` |
| A relation (`hasMany`, `manyToMany` or `Relation<...>`) | `<Type>.<field>: a relation (hasMany, manyToMany or relation) cannot be an array of arrays` |
| An indexed field (`@key`, `@unique`, `@searchField`) | `<Type>.<field>: an indexed field (@key, @unique or @searchField) cannot be an array of arrays` |
| An `@index` key | `<Type>: @index key "<field>" is an array of arrays, which cannot be an index column` |
| A DB column whose element type is another table (`Table[][]`) | `<Type>.<field>: an array of arrays of table type <Table> cannot be a relation; store a list of lists of its keys or of a @jsonField type` |
| A [projection](/superschematic/reference/projections/) column | `<Type>.<field>: projection columns cannot be arrays of arrays` |
| A projection's `@column`, `@join`, row rule or `collapse` key that reads one | `<Table>.<field> is an array of arrays, which a projection cannot read` |

## List rules

The generated Go, TypeScript and Python validators and the Go, TypeScript
and Python schema runtimes apply one set of rules, to `T[]` and to the
outer list of `T[][]` alike:

- **Required means present, not non-empty.** `[]` satisfies a required
  list, and so does an empty outer list. Declare non-emptiness with
  `listMin`.
- **Bounds apply to the outer list.** `listMin` and `listMax` count the
  inner lists; the inner lists have no bounds of their own.
- **Element constraints apply to every innermost element.** A field's
  `minLength`, `maxLength`, `pattern`, `min` and `max` are checked on each
  element of a `T[]` and each innermost element of a `T[][]`.
- **An inner list is never null.** An empty inner list is valid.
- **A list element is never null,** in a required and an optional list
  alike.

## Error paths

A validation error names the field and both indexes:

| Payload | Path | Code | Message |
| --- | --- | --- | --- |
| A required list of lists is absent or null | `field` | `required` | `required field` |
| An inner list is null | `field[i]` | `required` | `required field` |
| An inner value is not a list | `field[i]` | `type` | `expected an array` |
| An innermost element is null | `field[i][j]` | `required` | `required field` |
| An innermost element breaks its type's rule | `field[i][j]` | that rule's code, such as `enum`, `maxLength` or `min` | that rule's message |
| A nested object element has a bad field | `field[i][j].name` | that field's code | that field's message |
| The outer list is too short or too long | `field` | `listMin`, `listMax` | `must contain at least N items`, `must contain at most N items` |

The same paths appear where each target checks a payload:

- The Go API routes answer `400` with these paths for a request body.
- The Go, TypeScript and Python SDKs refuse a null inner list at `name[i]`
  and an element that fails its type's validation at `name[i][j]` before
  sending the request.
- pydantic's strict parse in the generated Python types refuses a bad enum
  or nested object element itself, at location `(field, i, j)`, before
  `validate_all` runs.
- The Rust types carry no validators: serde refuses a null inner list, and
  list bounds are not checked.

The known differences between the generated validators, and the vectors
every validator and runtime is tested against, are recorded in
[D12 and its amendment](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d12-arrays-of-arrays-one-flag-two-levels).

## Reading the IR

A list of lists is one `TypeRef` with `isArray` and `isArrayOfArrays`
set; `TypeRef.ArrayDepth()` returns 0, 1 or 2. A generator of your own
renders the element type wrapped `ArrayDepth()` times. A reader that does
not know `isArrayOfArrays` sees `T[][]` as `T[]`, so a tool that stores or
reads IR learns the key before a schema it handles uses nested lists, or
refuses such a schema with `Schema.FindArrayOfArrays()`.
