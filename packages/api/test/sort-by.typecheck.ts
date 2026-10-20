// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: SortBy names the item type's own fields, takes each of them or "-"
// and one, and is optional (D60, amended); @paginated's defaultSort is
// text.
import { HttpMethod, paginated, rest } from "@superschematic/api";
import type { SortBy } from "@superschematic/api";

type UUID = string & { readonly __brand: "Identity.UUID" };

export abstract class OrderView {
  id: UUID;
  placedAt: string;
  totalCents: number;
}

export class Orders {
  @rest(HttpMethod.GET, "orders")
  @paginated({ defaultLimit: 20, maxLimit: 100, defaultSort: "placedAt" })
  listOrders(sort: SortBy<OrderView, ["placedAt", "totalCents"]>): OrderView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "orders/by-number")
  // @ts-expect-error a field that is not one of the item's
  listByNumber(sort: SortBy<OrderView, ["number"]>): OrderView[] {
    throw new Error("schema declaration only");
  }

  // @ts-expect-error defaultSort is text
  @paginated({ defaultSort: 1 })
  numbered(): OrderView[] {
    throw new Error("schema declaration only");
  }
}

// A sort may be left out, or be a field, ascending or descending.
export const none: SortBy<OrderView, ["placedAt", "totalCents"]> = null;
export const ascending = "placedAt" as SortBy<OrderView, ["placedAt", "totalCents"]>;
export const descending = "-totalCents" as SortBy<OrderView, ["placedAt", "totalCents"]>;
// @ts-expect-error a sort is one of the fields it names
export const other: SortBy<OrderView, ["placedAt"]> = "totalCents" as SortBy<OrderView, ["totalCents"]>;
