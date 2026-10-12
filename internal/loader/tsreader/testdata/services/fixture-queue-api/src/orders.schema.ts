import { HttpMethod, rest, worker } from "@superschematic/api";
import { OrderPlaced, Ping } from "@schemas/fixture-queue-db";

export abstract class OrderView {
  id: string;
}

export class OrderQueries {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: string): OrderView {
    throw new Error("schema declaration only");
  }
}

// Fulfils each order placed, four at a time.
@worker({ queue: OrderPlaced, concurrency: 4, grace: "5s" })
export abstract class FulfilOrders {}

// Answers pings, one at a time.
@worker({ queue: Ping })
export abstract class AnswerPings {}
