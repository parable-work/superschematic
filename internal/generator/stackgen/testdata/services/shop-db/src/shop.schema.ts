import { Identity } from "superscalar";
import { key, queue } from "@superschematic/db";

export abstract class Product {
  @key
  id: Identity.UUID;

  name: string;
}

// Placed when an order is; shop-orders' worker FulfilOrders handles it.
@queue()
export abstract class OrderPlaced {
  orderId: Identity.UUID;
}
