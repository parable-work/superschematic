import { job } from "@superschematic/api";

// The supplier's order run: it files a restock request for each product
// whose Inventory is low (Stock.low) and has none open, which catches a
// count or an adjustment that ran it low with no sale, and then orders
// every open one. Each environment runs it every hour unless its settings
// change the schedule.
@job({ schedule: "0 * * * *", timeout: "5m", retries: 1 })
export abstract class OrderRestocks {}
