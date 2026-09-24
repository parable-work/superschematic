// Operation @docs from @superschematic/api and field @docs from
// @superschematic/schema in one file: the field decorator is imported under
// another name.
import { Identity } from "superscalar";
import { HttpMethod, docs, rest } from "@superschematic/api";
import { docs as fieldDocs, icon, purpose } from "@superschematic/schema";

export abstract class Order {
  id: Identity.UUID;

  @fieldDocs({ title: "Total" })
  @purpose("The order total in **cents**, tax included.")
  @icon("receipt")
  totalCents: number;
}

export abstract class ReturnRequest {
  orderId: Identity.UUID;
  reason: string;
}

export class OrderQueries {
  // Fetch one order. OpenAPI takes the @docs description instead.
  @docs({
    title: "Get an order",
    description: "Returns one order by its identifier.",
    capability: "orders.get",
    lifecycle: "active",
    visibility: "public",
    audience: "shoppers",
    useWhen: "Use when you have an order identifier.",
    doNotUseWhen: "Do not use to list orders; call listOrders.",
    success: "Returns the order with its total.",
    errors: [
      {
        code: "order_not_found",
        description: "No order has that identifier.",
        commonCorrection: "Take the identifier from a listOrders result."
      }
    ]
  })
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }

  // Lists every order. Without @docs the summary is the operation name.
  @rest(HttpMethod.GET, "orders")
  listOrders(): Order[] {
    throw new Error("schema declaration only");
  }
}

export class ReturnMutations {
  @docs({
    title: "Open a return",
    description: "Opens a return for one delivered order.",
    capability: "orders.returns.open",
    lifecycle: "deprecated",
    visibility: "preview",
    mappingStatus: "uncertain",
    replacement: "orders.returns.create",
    sunset: "2027-01-31"
  })
  @rest(HttpMethod.POST, "returns")
  openReturn(input: ReturnRequest): Order {
    throw new Error("schema declaration only");
  }
}
