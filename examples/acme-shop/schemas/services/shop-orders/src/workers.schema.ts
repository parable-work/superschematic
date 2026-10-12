import { worker } from "@superschematic/api";
import { OrderPlaced } from "@acme/shop-db";

// Fulfils each order as it is placed, four at a time: it marks the order
// fulfilled, ready for ShipOrders, the warehouse's pick run, to ship.
@worker({ queue: OrderPlaced, concurrency: 4 })
export abstract class FulfilOrders {}
