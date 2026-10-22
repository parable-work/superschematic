import { job } from "@superschematic/api";

// The warehouse's pick run: it ships every order FulfilOrders fulfilled.
// Each environment runs it every quarter hour unless its settings change
// the schedule.
@job({ schedule: "*/15 * * * *", timeout: "5m", retries: 1 })
export abstract class ShipOrders {}

// acmepay's adapter (D70): it reads the payment actions shop-db's engine
// holds for acmepay, the intents to create, the captures, voids and
// refunds, makes each at acmepay, with the action's id as its idempotency
// key, and reports the result back; it asks acmepay about attempts left
// processing; and it writes the order of each checkout placed since. Each
// environment runs it every five minutes unless its settings change the
// schedule.
@job({ schedule: "*/5 * * * *", timeout: "2m", retries: 1 })
export abstract class SettlePayments {}
