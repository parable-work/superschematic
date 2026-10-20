import { Nullable, Validate } from "@superschematic/schema";
import { HttpMethod, paginated, QueryParam, RefersTo, rest, SortBy } from "@superschematic/api";

export abstract class Item {
  name: string;
}

export abstract class Tool {
  id: string;
  name: Validate<string, { maxLength: 80 }>;
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

  // A sort that refers to the tools.
  @rest(HttpMethod.GET, "tools/referred")
  @paginated({ defaultSort: "name" })
  referredTools(sort: RefersTo<SortBy<Tool, ["name"]>, Tool, { options: "listTools" }>): Tool[] {
    throw new Error("schema declaration only");
  }

  // A sort with a Validate rule.
  @rest(HttpMethod.GET, "tools/validated")
  @paginated({ defaultSort: "name" })
  validatedTools(sort: Validate<SortBy<Tool, ["name"]>, { maxLength: 5 }>): Tool[] {
    throw new Error("schema declaration only");
  }

  // The tools, which the reference's options name.
  @rest(HttpMethod.GET, "tools")
  @paginated()
  listTools(): Tool[] {
    throw new Error("schema declaration only");
  }
}
