import { worker } from "@superschematic/api";
import { StockLow } from "@acme/shop-db";

// Files a restock request for each product a sale runs low, two at a time.
// A product has one open request at most, so a message that comes again,
// or one for a product whose request is still open, files none.
@worker({ queue: StockLow, concurrency: 2 })
export abstract class ReorderStock {}
