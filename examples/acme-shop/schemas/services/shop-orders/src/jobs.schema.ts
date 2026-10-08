import { job } from "@superschematic/api";

// The warehouse's pick run: it ships every placed order. Each environment
// runs it every quarter hour unless its settings change the schedule.
@job({ schedule: "*/15 * * * *", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}
