---
title: References and search
description: Say which item a field or argument names with RefersTo, and which argument is a list's search with SearchQuery; what each derives, what the loader refuses, and the runtime helpers a search uses.
sidebar:
  order: 10
---

Two type wrappers from `@superschematic/api` describe an API for the UIs
that read it. Neither changes a server, an SDK or an OpenAPI document: the
argument or field keeps its own type and its own rules.

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
