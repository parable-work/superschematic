import { Identity } from "superscalar";
import { Nullable, Validate, display } from "@superschematic/schema";
import { HttpMethod, QueryParam, RefersTo, SearchQuery, rest } from "@superschematic/api";

// An item as the catalog lists it: its title, and so the label of every
// reference to it, is its name.
@display({ noun: "Item", plural: "Items", titleField: "name", summaryFields: ["name", "sku"] })
export abstract class ItemView {
  id: Identity.UUID;
  name: string;
  sku: Identity.Slug;
  inStock: boolean;
}

// A line names its item by id, which listItems lists.
export abstract class BasketLine {
  itemId: RefersTo<Identity.UUID, ItemView>;
  quantity: Validate<number, { min: 1; max: 9 }>;
}

export abstract class BasketInput {
  lines: Validate<BasketLine[], { listMin: 1; listMax: 5 }>;
  // A gift named by its sku, with every member said.
  giftSku: Nullable<RefersTo<Identity.Slug, ItemView, { options: "Catalog.listItems"; value: "sku"; label: "name" }>>;
}

export abstract class BasketView {
  id: Identity.UUID;
  lines: BasketLine[];
}

// The kinds of item the catalog shelves.
export enum ItemKind {
  Book = "book",
  Game = "game"
}

// A kind as the catalog lists it, by its code: a reference to it holds an
// enum's value, whose schema is a component.
@display({ noun: "Kind", plural: "Kinds", titleField: "name" })
export abstract class KindView {
  code: ItemKind;
  name: string;
}

// Where an item is shelved: under a kind, named by its code.
export abstract class ShelfEntry {
  itemId: RefersTo<Identity.UUID, ItemView>;
  kind: RefersTo<ItemKind, KindView, { value: "code" }>;
}

export class Catalog {
  // The items, narrowed by a search of their text and by ids, the lookup
  // a UI names the references' values through.
  @rest(HttpMethod.GET, "items")
  listItems(
    q: SearchQuery<Validate<string, { maxLength: 50 }>>,
    ids: QueryParam<Nullable<Validate<RefersTo<Identity.UUID, ItemView>[], { listMax: 5 }>>>,
    inStock: QueryParam<Nullable<boolean>>
  ): ItemView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "items/{itemId}")
  getItem(itemId: RefersTo<Identity.UUID, ItemView>): ItemView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "baskets")
  placeBasket(input: BasketInput): BasketView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "kinds")
  listKinds(): KindView[] {
    throw new Error("schema declaration only");
  }

  // Shelves an item under a kind, a body argument that names it by its
  // code.
  @rest(HttpMethod.POST, "items/{itemId}/shelf")
  shelveItem(itemId: RefersTo<Identity.UUID, ItemView>, kind: RefersTo<ItemKind, KindView, { value: "code" }>): ShelfEntry {
    throw new Error("schema declaration only");
  }
}
