import { job } from "@superschematic/api";

// The supplier's order run: it orders every restock request ReorderStock
// filed and the run finds open. Each environment runs it every hour unless
// its settings change the schedule.
@job({ schedule: "0 * * * *", timeout: "5m", retries: 1 })
export abstract class PlaceRestockOrders {}
