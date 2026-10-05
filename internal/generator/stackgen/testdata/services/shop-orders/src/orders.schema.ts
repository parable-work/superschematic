import { HttpMethod, rest } from "@superschematic/api";

export abstract class OrderView {
  id: string;
}

export class OrderQueries {
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: string): OrderView {
    throw new Error("schema declaration only");
  }
}
