import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source, tableOperation } from "@superschematic/api";
import { Order } from "@schemas/fixture-workflow-db";

// An order as a list shows it: its title and where its status Workflow
// stands, the fields the Workflow owns, which only its operations change.
@source(Order)
export abstract class OrderView {
  id: Identity.UUID;
  title: string;
  status: string;
  statusEnteredAt?: Temporal.DateTime;
}

// What a caller sets to place an order: its title and, when it is not a
// draft, the state it starts in.
@source(Order)
export abstract class PlaceOrderInput {
  title: string;
  status: Nullable<string>;
}

export class OrderViews extends Authenticated {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrderView(id: Identity.UUID): OrderView {
    throw new Error("schema declaration only");
  }

  // Places an order. A body that names a state creates the order in its
  // Workflow's initial state and moves it there by the Workflow's own
  // transition, in one transaction and as the caller (D109).
  @rest(HttpMethod.POST, "orders")
  @tableOperation()
  placeOrder(input: PlaceOrderInput): OrderView {
    throw new Error("schema declaration only");
  }
}
