import { Identity, Temporal } from "superscalar";
import { AutoGenerate, Workflow, index, key, unique } from "@superschematic/db";
import { paymentFlow } from "./flows";

// An order a customer places, which moves from draft through paid to
// shipped. It stays a table: its status is a Workflow, whose fields are
// its columns (D103), which an index names as it names its own.
@index<Order>(["status", "placedAt"])
export abstract class Order {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{
    states: ["draft", "paid", "shipped"];
    initial: "draft";
    transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
  }>;
  placedAt?: Temporal.DateTime;
}

// A shipment of goods, which is packed and sent while its payment settles
// apart: two Workflows, one on each of two fields.
export abstract class Shipment {
  @key
  id: AutoGenerate<Identity.UUID>;
  @unique
  reference: string;
  status: Workflow<{
    states: readonly ["pending", "packed", "sent"];
    transitions: readonly [{ readonly from: "pending"; readonly to: "packed" }, { from: "packed"; to: "sent" }];
  }>;
  paymentStatus: Workflow<typeof paymentFlow>;
}
