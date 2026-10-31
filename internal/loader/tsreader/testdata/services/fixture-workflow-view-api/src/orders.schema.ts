import { Identity, Temporal } from "superscalar";
import { Authenticated, HttpMethod, rest, source } from "@superschematic/api";
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

export class OrderViews extends Authenticated {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrderView(id: Identity.UUID): OrderView {
    throw new Error("schema declaration only");
  }
}
