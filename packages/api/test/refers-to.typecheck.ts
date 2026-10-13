// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: RefersTo's config names the target's own fields for value and label
// (D61), and SearchQuery takes text and is optional (D62).
import type { QueryParam, RefersTo, SearchQuery } from "@superschematic/api";
import type { Validate } from "@superschematic/schema";

type UUID = string & { readonly __brand: "Identity.UUID" };

export abstract class ItemView {
  id: UUID;
  sku: string;
  name: string;
}

export abstract class BasketLine {
  itemId: RefersTo<UUID, ItemView>;
  bySku: RefersTo<string, ItemView, { value: "sku"; label: "name" }>;
  listed: RefersTo<UUID, ItemView, { options: "Catalog.listItems" }>[];
  // @ts-expect-error value names a field of the target
  byCode: RefersTo<string, ItemView, { value: "code" }>;
  // @ts-expect-error label names a field of the target
  titled: RefersTo<UUID, ItemView, { label: "title" }>;
  // @ts-expect-error options is an operation's name, a string
  numbered: RefersTo<UUID, ItemView, { options: 1 }>;
  // @ts-expect-error an unknown key
  sorted: RefersTo<UUID, ItemView, { order: "name" }>;
}

export class Catalog {
  listItems(
    q: SearchQuery<Validate<string, { maxLength: 50 }>>,
    ids: QueryParam<RefersTo<UUID, ItemView>[] | null>
  ): ItemView[] {
    throw new Error("schema declaration only");
  }

  search(q: SearchQuery): ItemView[] {
    throw new Error("schema declaration only");
  }

  // @ts-expect-error a search is text
  byCount(q: SearchQuery<number>): ItemView[] {
    throw new Error("schema declaration only");
  }
}

// A search argument may be left out.
export const none: SearchQuery = null;
export const text: SearchQuery = "red anvil" as SearchQuery;
