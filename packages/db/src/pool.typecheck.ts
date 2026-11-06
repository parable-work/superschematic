// Type-level checks of Pool<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Pool, PoolAvailability, PoolOn } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A
// person's accounts, ranked by rank, out for thirty minutes when a
// provider names no time and rechecked every two minutes, marked out and
// checked by callers holding accounts.pool.
export abstract class Account implements
  Pool<Account, {
    order: "rank";
    outForMs: 1800000;
    recheckAfterMs: 120000;
    permission: "accounts.pool";
  }>
{
  id: string;
  owner: string;
  vendor: string;
  rank: number;
}

// The fewest members: order, a field of the table.
const fewest: PoolOn<Account> = { order: "rank" };
const every: PoolOn<Account> = { order: "rank", outForMs: 600000, recheckAfterMs: 60000, permission: "accounts.pool" };
const availability: readonly PoolAvailability[] = ["open", "near", "out"];

// order is needed and names a field of the table; a config names no member
// but its own; outForMs is a number.
// @ts-expect-error Pool names the field that ranks its members
type NoOrder = Pool<Account, { outForMs: 1800000 }>;
// @ts-expect-error order names a field of the table
type NoSuchField = Pool<Account, { order: "score" }>;
// @ts-expect-error a config names no member but order, outForMs, recheckAfterMs and permission
const unknownMember: PoolOn<Account> = { order: "rank", ttlMs: 1000 };
// @ts-expect-error outForMs is a number of milliseconds
type ThirtyMinutes = Pool<Account, { order: "rank"; outForMs: "30m" }>;
// @ts-expect-error an availability is open, near or out
const closed: PoolAvailability = "closed";

// An engine type names no Pool: @superschematic/schema's BehaviorConfigs
// has none, since no engine implements it.
// @ts-expect-error Pool is a table's alone
@behavior("Pool", { order: "rank" })
export abstract class EngineAccount {}

export const checked: readonly unknown[] = [fewest, every, availability, unknownMember, closed];
export type Refused = NoOrder | NoSuchField | ThirtyMinutes;
