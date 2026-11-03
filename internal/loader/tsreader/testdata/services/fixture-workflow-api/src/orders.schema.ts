import { Identity } from "superscalar";
import { HttpMethod, auth, behaviorOperations, requirePermission, rest } from "@superschematic/api";
import { Order, Shipment } from "@schemas/fixture-workflow-db";

// An order's status operations: each transition its Workflow lists, run
// through the behaviors' core (D103). The route needs orders.write; a move
// to shipped needs orders.ship too, which the core checks.
@behaviorOperations(Order)
@requirePermission(["orders.write"])
export abstract class OrderActions {}

// A shipment's two Workflows, status and paymentStatus, for any signed-in
// caller; a refund needs payments.refund, which the core checks.
@behaviorOperations(Shipment)
@auth
export abstract class ShipmentActions {}

// An order as its table holds it, the fields its status Workflow owns
// among them.
export class OrderQueries {
  @auth
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }
}
