// MCP tools: a visible tool takes its title and description from @docs and
// its icon from @icon; a hidden operation says why it is not a tool; an
// operation without @mcp is not classified and is not published.
import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { HttpMethod, QueryParam, docs, icon, mcp, rest } from "@superschematic/api";

export enum OrderStatus {
  Open = "open",
  Shipped = "shipped"
}

export abstract class Address {
  line1: string;
  city: string;
  postalCode: Nullable<string>;
}

export abstract class Order {
  id: Identity.UUID;
  status: OrderStatus;
  revision: number;
  totalCents: number;
}

export abstract class ReturnRequest {
  requestId: Identity.UUID;
  orderId: Identity.UUID;
  reason: Validate<string, { minLength: 1; maxLength: 500 }>;
  pickup: Address;
  labels: Record<string, string>;
}

export abstract class OrderUpdate {
  revision: number;
  note: Nullable<string>;
}

export class OrderQueries {
  // Fetch one order.
  @docs({
    title: "Get an order",
    description: "Returns one order by its identifier.",
    capability: "orders.get",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers",
    replayMode: "read_only",
    useWhen: "Use when you have an order identifier.",
    doNotUseWhen: "Do not use to list orders; call list_orders.",
    success: "Returns the order with its status and total.",
    errors: [
      {
        code: "order_not_found",
        description: "No order has that identifier.",
        commonCorrection: "Take the identifier from a list_orders result."
      }
    ]
  })
  @icon("receipt")
  @mcp({ handle: "get_order", _meta: { ui: { resourceUri: "ui://orders/detail" } } })
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }

  @docs({
    title: "List orders",
    description: "Lists orders by status.",
    capability: "orders.list",
    lifecycle: "active",
    visibility: "public",
    replayMode: "read_only"
  })
  @icon("list")
  @mcp({ handle: "list_orders" })
  @rest(HttpMethod.GET, "orders")
  listOrders(
    statuses: QueryParam<Validate<OrderStatus[], { listMin: 1; listMax: 2 }>>,
    limit: QueryParam<Nullable<Validate<number, { min: 1; max: 100 }>>>
  ): Order[] {
    throw new Error("schema declaration only");
  }

  // Not classified: no @mcp, so no tool.
  @rest(HttpMethod.GET, "orders/export")
  exportOrders(): string {
    throw new Error("schema declaration only");
  }
}

export class OrderMutations {
  @docs({
    title: "Open a return",
    description: "Opens a return for one delivered order.",
    capability: "orders.returns.open",
    lifecycle: "active",
    visibility: "public",
    replayMode: "idempotent",
    idempotencyKeyPointers: ["/requestId"]
  })
  @icon("box")
  @mcp({ handle: "open_return" })
  @rest(HttpMethod.POST, "returns")
  openReturn(input: ReturnRequest): Order {
    throw new Error("schema declaration only");
  }

  @docs({
    title: "Update an order",
    description: "Changes an order's note when its revision still matches.",
    capability: "orders.update",
    lifecycle: "active",
    visibility: "public",
    replayMode: "compare_and_swap",
    expectedRevisionPointers: ["/revision"]
  })
  @icon("pen")
  @mcp({ handle: "update_order" })
  @rest(HttpMethod.PATCH, "orders/{id}")
  updateOrder(id: Identity.UUID, input: OrderUpdate): Order {
    throw new Error("schema declaration only");
  }

  @mcp({ hidden: true, reason: "Staff console only." })
  @rest(HttpMethod.DELETE, "orders/{id}")
  deleteOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }
}
