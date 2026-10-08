import { HttpMethod, job, rest } from "@superschematic/api";

export abstract class OrderView {
  id: string;
}

export class OrderQueries {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: string): OrderView {
    throw new Error("schema declaration only");
  }
}

// The warehouse's pick run: ships each placed order.
@job({ schedule: "*/15 * * * *", timeZone: "Europe/Paris", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}

// Runs only on demand.
@job()
export abstract class ReindexOrders {}
