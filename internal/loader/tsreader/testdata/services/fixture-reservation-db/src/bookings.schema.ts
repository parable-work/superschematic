import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Calendar, Relation, Reservation, key } from "@superschematic/db";

// A room whose time Calendar opens (D70): weekday hours in Europe/London,
// one booking at a time, on a quarter-hour grid, of a quarter of an hour
// to two hours. The resource a booking's Reservation holds.
export abstract class Room
  implements
    Calendar<Room, {
      timeZone: "Europe/London";
      hours: {
        mon: [["09:00", "17:30"]];
        tue: [["09:00", "17:30"]];
        wed: [["09:00", "17:30"]];
        thu: [["09:00", "17:30"]];
        fri: [["09:00", "17:30"]];
      };
      capacity: 1;
      slotMs: 900000;
      minMs: 900000;
      maxMs: 7200000;
      permission: "rooms.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

// A booking of a room (D103, step 5): a Reservation, which says only its
// resource, its interval, its amount and its currency. The bundle adds its
// status Workflow, its Holds on the room's time, its Payments and the
// Timers deadline that expires a hold.
export abstract class Booking
  implements
    Reservation<Booking, {
      resource: "room";
      interval: { start: "startsAt"; end: "endsAt" };
      amount: "priceCents";
      currency: "GBP";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  guest: string;
  startsAt: Temporal.DateTime;
  endsAt: Temporal.DateTime;
  priceCents: Generic.Int64;
  room: Nullable<Relation<Room>>;
}
