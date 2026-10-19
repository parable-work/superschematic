---
title: References and search
description: Say which item a field or argument names with RefersTo, and which argument is a list's search with SearchQuery; what each derives, what the loader refuses, and the runtime helpers a search uses.
sidebar:
  order: 10
---

Two type wrappers from `@superschematic/api` describe an API for the UIs
that read it. Neither changes a server or an SDK: the argument or field
keeps its own type and its own rules. The OpenAPI document and the tool
documents describe both ([In the documents](#in-the-documents)).

## `RefersTo`: a value that names an item

`RefersTo<T, Target, { options, value, label }>` says that a value of type
`T` names an item of `Target`, as an operation of the same API lists it
(D61):

```ts
import { Identity } from "superscalar";
import { Nullable, Validate, display } from "@superschematic/schema";
import { HttpMethod, QueryParam, RefersTo, rest } from "@superschematic/api";

@display({ noun: "Product", titleField: "name" })
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: string;
}

export abstract class OrderLineInput {
  productId: RefersTo<Identity.UUID, ProductView>;
  // Every member said: the operation, the field the value holds, the field that names an item.
  giftSku: Nullable<RefersTo<Identity.Slug, ProductView, { options: "Catalog.listProducts"; value: "sku"; label: "name" }>>;
}

export class Catalog {
  @rest(HttpMethod.GET, "products")
  listProducts(
    ids: QueryParam<Nullable<Validate<RefersTo<Identity.UUID, ProductView>[], { listMax: 100 }>>>
  ): ProductView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "products/{productId}")
  getProduct(productId: RefersTo<Identity.UUID, ProductView>): ProductView {
    throw new Error("schema declaration only");
  }
}
```

It goes on a field of an API schema's type, input or output, or on an
operation's argument. A list refers element by element, written either
way: `RefersTo<Identity.UUID, ProductView>[]` or
`RefersTo<Identity.UUID[], ProductView>`. The TypeScript checker holds
`value` and `label` to the target's own field names.

Each member left out is derived, by `ir.ResolveReference` in the public
`ir` module, which the loader and the generators call:

| Member | Unsaid |
| --- | --- |
| `options` | the API's one operation that lists the target: a `GET` returning `Target[]`, one level, with no path argument and no required one, that an end user may call (not `@requireService`, `@manualRouteRegistration` or `@webhook`). Named, it is `listProducts`, or `Catalog.listProducts` when the name alone is not unique, and must meet the same rules |
| `value` | `id`: the target's field the value holds, one value of the reference's own type |
| `label` | the target's `@display` `titleField`, else none, and a UI shows the value. Named, it follows `titleField`'s rule: one text value, not secret, not `@uiHidden` |

The options operation's lookup is its optional list query argument that
refers to the same target by the same value (`ids` above); a UI calls it
with the values it must name. An operation has at most one.

The JSON and YAML forms carry `references` on the field or argument, the
members as written:

```yaml
- name: productId
  typeRef: { name: Identity.UUID }
  required: true
  references: { type: ProductView }
- name: giftSku
  typeRef: { name: Identity.Slug }
  references: { type: ProductView, options: Catalog.listProducts, value: sku, label: name }
```

The loader refuses, in every form, with the field (`Type.field`) or the
argument (`Set.operation(argument)`) named first:

- a reference outside an API schema, on an operation's result, on a map or
  on a list of lists;
- a target that is not an object type of the service or of a dependency
  (an enum, a union, a scalar or a trait);
- no operation that lists the target, or two (`name one with options`),
  and an `options` that is no operation of the API, does not list the
  target, or has a path or required argument, admits only services, or is
  manual or a webhook;
- a `value` that is not a field of the target holding one value of the
  reference's type (`is Identity.UUID, but ProductView.sku, its value, is
  Identity.Slug`), and a `label` that is not one text field;
- two lookups on the options operation.

A reference is not checked at run time: an implementation refuses an id it
cannot find, as it does without one.

A Topcoat crate offers a reference's items as a select in its forms and
names each value by its item's label in its display components, through
the options operation and its lookup
([Choices from references](/superschematic/guides/topcoat/#choices-from-references)).

## What a value refers to

A field or argument refers to what its `RefersTo` names. On a type with
`@source`, a relation's key refers to an item with no `RefersTo` written
(D61, amended):

```ts
import { Identity } from "superscalar";
import { display } from "@superschematic/schema";
import { source } from "@superschematic/api";
import { OrderLine, Product } from "@acme/shop-db";

@source(Product)
@display({ noun: "Product", titleField: "name" })
export abstract class ProductView {
  id: Identity.UUID;
  name: string;
}

@source(OrderLine)
export abstract class OrderLineView {
  id: Identity.UUID;
  // OrderLine.product is a Relation<Product>, and ProductView is the
  // API's one type that is @source(Product): this refers to it.
  productId: Identity.UUID;
}
```

A field is a relation's key when it is on a type with `@source(T)`, is
named `<r>Id`, and `T` declares no field of that name, where `T`'s field
`r` is one `Relation<U>`, not `HasMany` and not `ManyToMany`. It reads
`r`'s foreign key, the column `<r>_id`, so it needs no `@virtual`.
`@virtual` still makes it a field the implementation fills. Its type is
the type of `U`'s `id`, one value. A `@virtual` field named `<r>Id` that
does not hold one value of that type is no key: it refers to nothing, the
loader refuses nothing of it, and the implementation fills it, as it fills
any `@virtual` field.

A key that writes no `RefersTo` refers to the API's one object type that
is `@source(U)`, by its `id`. Every other member is unsaid, and
`ir.ResolveReference` derives it, as for a written one. The reference
exists only when it resolves:

- With no such type, the key is a plain value.
- With such a type that no operation lists, or that two operations list,
  the key is a plain value.
- With two or more such types, the key is a plain value, and the loader
  warns: `OrderLineView.productId reads the key of shop-db.OrderLine.product
  and refers to nothing: ProductCard and ProductView are both
  @source(shop-db.Product); name one with RefersTo`.

A written `RefersTo` always wins, with its members as written. An argument
or an input type has no `@source`, so it says `RefersTo`.

The reference is derived each time it is read and never written into the
IR, so `format` writes the key as its type alone, and the JSON and YAML
forms carry no `references` on it. In the public `ir` module,
`ir.FieldReference(schema, dependencies, holder, field)` gives the
reference a field holds and says whether it was derived.
`ir.DerivedReferences(schema, dependencies)` lists the derived ones, as
`ir.ReferencesOf(schema)` lists the written ones. The dependencies hold
the table types that the `@source` names. The loader and the generators
read references through these functions, not through the field's
`references`.

Where its `@source` checks run, the loader refuses three things:

- a key, not `@virtual`, whose type is not the type of `U`'s id:
  `OrderLineView.productId reads the key of shop-db.OrderLine.product, a
  Relation to Product, whose id is Identity.UUID, not string`;
- a key, not `@virtual`, that is a list or a map;
- a `RefersTo` on a key that names a type that is `@source` of another
  table, the API's own or one it imports: `OrderLineView.productId:
  RefersTo names SupplierView, which is @source(shop-db.Supplier), but the
  field reads the key of shop-db.OrderLine.product, a Relation to
  Product`.

The data forms skip these checks, as they skip the other `@source` checks.
`migrate` counts a key as a read of its foreign-key column, as it counts
the relation field.

A reference's target need not be the API's own type. It may be a
dependency's. That type is found through the schema's import that names
it, else by name in each dependency, in name order. So two dependencies
that declare types of one name resolve one reference to one type, the one
the schema imports, in the loader and in every generator (D66).

A type of an API dependency that the API's operations take or return may
hold a `RefersTo` of its own, which names its own API's operations. In
the consuming API, the documents and the console read it against the
API's operations, and keep it only when it resolves there to a value of
the field's type. Otherwise the field is a plain value.

## `SearchQuery`: a list's search text

`SearchQuery<T>` marks an argument as its operation's search text (D62).
It is an optional query parameter, as `QueryParam<Nullable<T>>` is, whose
`T` is `string` or a scalar whose values are strings, with its own rules:

```ts
@rest(HttpMethod.GET, "reviews")
listReviews(
  q: SearchQuery<Validate<string, { maxLength: 100 }>>,
  minRating: QueryParam<Nullable<number>>
): ReviewView[] {
  throw new Error("schema declaration only");
}
```

A bare `SearchQuery` is `SearchQuery<string>`. The data forms write
`isQuery: true` and `search: true`, without `required`. The loader refuses
a search argument that is required, has a default, is not text or is a
list, sits on an operation that is not a `GET`, or is the operation's
second.

A search keeps the items whose searchable text contains each of its words,
case-insensitively, and does not rank them. A word is a run of characters
that are not Unicode `White_Space`; the first 8 count, and a blank search
keeps every item. Each server's runtime gives the steps an implementation
needs, held to one set of answers by
`runtime/http/testdata/search_parity.json`:

| Step | Go (`runtime/http/go/search`) | TypeScript (`@superschematic/http-runtime`) | Rust (`superschematic_http_runtime::search`) |
| --- | --- | --- | --- |
| The words | `search.Terms(q)` | `searchTerms(q)` | `search::terms(q)` |
| A word's `LIKE` pattern: `\`, `%` and `_` escaped with `\`, wrapped in `%` | `search.LikePattern(w)` | `likePattern(w)` | `search::like_pattern(w)` |
| An in-memory match, every word of the folded text | `search.Matches(text, terms)` | `searchMatches(text, terms)` | `search::matches(text, &terms)` |
| The fold: each character's simple lowercase | `search.Fold(s)` | `foldText(s)` | `search::fold(s)` |

Over SQL each word is one condition, all of them ANDed:
`search_text ILIKE $1 ESCAPE '\'` on Postgres, which the trigram index on
a table's [`search_text`](/superschematic/guides/database-tables/#text-search)
serves, and `search_text LIKE ?1 ESCAPE '\'` on SQLite, which reads every
row and folds case in ASCII alone.

A Topcoat crate renders the search argument as its argument form's first
control, an `<input type="search">` labeled Search, and the console's
filter form that holds it as a search form
([A paged list and a search box](/superschematic/guides/topcoat/#a-paged-list-and-a-search-box)).

## In the documents

The OpenAPI document and the SDKs' tool documents describe each reference
and the search with vendor keys, which a client reads as rules (D61 and
D62, amended). A reference is written resolved, every member derived,
whether `RefersTo` writes it or a relation's key derives it. A derived
reference that does not resolve is no reference and is not written.

In the OpenAPI document, the value that refers gives
`x-superschematic-reference`:

```json
"x-superschematic-reference": {
  "target": "#/components/schemas/ProductView",
  "options": "CatalogListProductsHandler",
  "value": "id",
  "label": "name",
  "lookup": "ids"
}
```

| Member | What it holds |
| --- | --- |
| `target` | the target's component |
| `options` | the `operationId` of the operation that lists the target |
| `value` | the target's key the value holds |
| `label` | the target's key that names an item; absent when it has none |
| `lookup` | the options operation's argument that narrows its list to the values given; absent when it has none |

The key sits on the property schema of a field (of an output or an input
type) and of a body argument, and on the parameter object of a path or
query argument, beside its `schema`. A list carries it on the list, not on
its items, and each item refers. The search argument's parameter object
says `"x-superschematic-search": true`:

```json
{
  "name": "q", "in": "query", "required": false,
  "schema": {"type": "string", "maxLength": 100},
  "x-superschematic-search": true
}
```

The keys are `apigen.OpenAPIReferenceKey` and `apigen.OpenAPISearchKey`.
An OpenAPI hook renames them, as it renames `x-superschematic-docs`. A
standalone type schema (`apigen.TypeOpenAPISchema`) carries no reference,
since the operation it would name is in the API's document.

A tool's parameters in `tools/schema.json`, the provider lists and
`tools/index.ts` give each argument's or field's property the same keys,
after `x-superschematic-scalar`. There `target` is the type's name and
`options` the tool name of the options operation (`catalog.listProducts`).
A tool hook renames the keys through `ToolKeys.Reference` and
`ToolKeys.Search`
([MCP tools](/superschematic/reference/mcp-tools/#toolsschemajson)). In
Go, `ir.ToolSchemaProperty` decodes them as `Reference`, an
`ir.ToolSchemaReference`, and `Search`.

Neither key changes what a server accepts. A reference is not checked at
run time, and a search keeps its rule.
