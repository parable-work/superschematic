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
}
