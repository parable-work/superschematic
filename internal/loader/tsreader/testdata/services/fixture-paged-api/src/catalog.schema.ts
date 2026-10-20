import { Identity } from "superscalar";
import { display, Nullable, Validate } from "@superschematic/schema";
import { HttpMethod, paginated, QueryParam, rest, SortBy } from "@superschematic/api";

// An item of the catalog.
@display({ noun: "Item", plural: "Items", titleField: "name", summaryFields: ["name", "sku"] })
export abstract class ItemView {
  id: Identity.UUID;
  name: string;
  sku: Identity.Slug;
  inStock: boolean;
}

// One line of a basket: an item and how many of it.
export abstract class BasketLine {
  itemId: Identity.UUID;
  quantity: Validate<number, { min: 1; max: 9 }>;
}

// Request body: the lines of a basket to place.
export abstract class BasketInput {
  lines: Validate<BasketLine[], { listMin: 1; listMax: 5 }>;
}

// A placed basket.
export abstract class BasketView {
  id: Identity.UUID;
  lines: BasketLine[];
}

export class Catalog {
  // The items, two to a page unless the caller asks for up to five, by
  // their ids or their stock, in order of their names unless the caller
  // sorts them by name or sku, either way.
  @rest(HttpMethod.GET, "items")
  @paginated({ defaultLimit: 2, maxLimit: 5, defaultSort: "name" })
  listItems(
    ids: QueryParam<Nullable<Validate<Identity.UUID[], { listMax: 5 }>>>,
    inStock: QueryParam<Nullable<boolean>>,
    sort: SortBy<ItemView, ["name", "sku"]>
  ): ItemView[] {
    throw new Error("schema declaration only");
  }

  // One item.
  @rest(HttpMethod.GET, "items/{itemId}")
  getItem(itemId: Identity.UUID): ItemView {
    throw new Error("schema declaration only");
  }

  // The tags items carry: a page of a primitive, with the default limits.
  @rest(HttpMethod.GET, "tags")
  @paginated()
  listTags(): string[] {
    throw new Error("schema declaration only");
  }

  // Place a basket.
  @rest(HttpMethod.POST, "baskets")
  placeBasket(input: BasketInput): BasketView {
    throw new Error("schema declaration only");
  }
}
