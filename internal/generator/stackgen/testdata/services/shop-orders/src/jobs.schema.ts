import { job } from "@superschematic/api";

// The warehouse's pick run: ships each placed order.
@job({ schedule: "*/15 * * * *", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}
