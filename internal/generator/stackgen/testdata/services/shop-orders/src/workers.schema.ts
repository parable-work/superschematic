import { worker } from "@superschematic/api";
import { OrderPlaced } from "@schemas/shop-db";

// Fulfils each order placed, four at a time.
@worker({ queue: OrderPlaced, concurrency: 4 })
export abstract class FulfilOrders {}
