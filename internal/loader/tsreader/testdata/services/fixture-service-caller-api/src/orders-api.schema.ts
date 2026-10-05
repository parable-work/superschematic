import { HttpMethod, auth, rest } from "@superschematic/api";

// An order. The server that serves this API calls fixture-service-auth-api,
// whose operations name it in from by importing this service's sentinel.
export abstract class Order {
  id: string;
  placed: boolean;
}

export class OrderQueries {
  // One order.
  @auth
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: string): Order {
    throw new Error("schema declaration only");
  }
}
