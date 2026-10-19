import { Nullable } from "@superschematic/schema";
import { HttpMethod, paginated, QueryParam, rest, SortBy } from "@superschematic/api";

export abstract class Item {
  name: string;
}

export class Catalog {
  // A page of a POST.
  @rest(HttpMethod.POST, "items/search")
  @paginated()
  searchItems(): Item[] {
    throw new Error("schema declaration only");
  }

  // An argument the page's own limit would shadow.
  @rest(HttpMethod.GET, "items")
  @paginated({ maxLimit: 50 })
  listItems(limit: QueryParam<Nullable<number>>): Item[] {
    throw new Error("schema declaration only");
  }

  // The name the SDKs give listItems' iterator.
  @rest(HttpMethod.GET, "items/all")
  listItemsAll(): Item[] {
    throw new Error("schema declaration only");
  }

  // A page of one item.
  @rest(HttpMethod.GET, "items/first")
  @paginated()
  firstItem(): Item {
    throw new Error("schema declaration only");
  }

  // A sort of a list that does not page, of items without an id.
  @rest(HttpMethod.GET, "items/sorted")
  sortedItems(sort: SortBy<Item, ["name"]>): Item[] {
    throw new Error("schema declaration only");
  }

  // An order for a page that sorts by nothing.
  @rest(HttpMethod.GET, "items/ordered")
  @paginated({ defaultSort: "name" })
  orderedItems(): Item[] {
    throw new Error("schema declaration only");
  }
}
