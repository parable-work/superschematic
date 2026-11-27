// Type-level checks of the core's bundles (D103, step 5): Reservation,
// Barrier, Mailbox and Assignment. Nothing imports this file: `bun run
// typecheck`, which `make ts` runs, checks it, and fails on an expected
// error (@ts-expect-error) that no longer errors.
import type { Assignment, Barrier, Bundle, Mailbox, Reservation, ReservationSettings } from "./index";
import type { Seats, Workflow } from "./index";

// A bundle imposes no members: a table implements it with its own fields
// alone, and the first type argument is the table itself.
export abstract class Booking implements
  Reservation<Booking, { resource: "room"; interval: { start: "startsAt"; end: "endsAt" }; amount: "priceCents"; currency: "GBP" }>
{
  startsAt: string;
  endsAt: string;
  priceCents: number;
  room: string;
}

export abstract class Gate implements
  Barrier<Gate, { rows: "Agent"; link: "gate"; timeoutMs: 300000; releasePermission: "gates.release" }>
{
  title: string;
}

export abstract class Message implements Mailbox<Message, { ttlMs: 30000 }> {
  createdBy: string;
}

export abstract class Chore implements Assignment<Chore, {}> {
  title: string;
}

// A project's own bundle: its parts' carriers with the table, a setting
// read in place.
type Owned<T, S extends { take: string }> = Bundle<
  [Seats<T, { seats: { owner: { max: 1; take: S["take"] } } }>],
  { state: Workflow<{ states: ["open", "done"]; transitions: [{ from: "open"; to: "done" }] }> }
>;
export abstract class Task implements Owned<Task, { take: "tasks.take" }> {
  title: string;
}

const settings: ReservationSettings<Booking> = { resource: "room", interval: { start: "startsAt", end: "endsAt" }, amount: "priceCents", currency: "GBP" };

// A setting names a field of the table; every required setting is given;
// a setting is of its type; no setting is one the bundle lacks.
// @ts-expect-error resource names a field of the table
const noField: ReservationSettings<Booking> = { resource: "desk", interval: { start: "startsAt", end: "endsAt" }, amount: "priceCents", currency: "GBP" };
// @ts-expect-error Reservation's settings name the interval
type NoInterval = Reservation<Booking, { resource: "room"; amount: "priceCents"; currency: "GBP" }>;
// @ts-expect-error timeoutMs is a number
type TimeoutText = Barrier<Gate, { rows: "Agent"; link: "gate"; timeoutMs: "5m"; releasePermission: "gates.release" }>;
// @ts-expect-error a part's config is its behavior's
type BadPart = Bundle<[Seats<Chore, { seats: { owner: { onFull: "wait" } } }>]>;

export const checked: readonly unknown[] = [settings, noField];
export type Refused = NoInterval | TimeoutText | BadPart;
