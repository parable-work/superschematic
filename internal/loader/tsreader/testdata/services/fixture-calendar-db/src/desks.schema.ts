import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Calendar, Holds, Relation, Workflow, key } from "@superschematic/db";

// A desk whose time Calendar opens (D70, D103's step 4): weekday hours in
// Europe/London, one booking at a time, a quarter-hour grid, holds of a
// quarter of an hour to two hours, and its own hours and closures set by a
// caller holding desk.manage. Its record of each booking it holds, its
// closures and its own settings are sub-tables of the row.
export abstract class Desk
  implements
    Calendar<Desk, {
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
      permission: "desk.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}

// A booking that holds a slot of its desk while it waits (D70, D103's step
// 4): entering held takes the slot through the contract Supply the desk
// implements, booked commits it, expired and cancelled let it go.
export abstract class Booking
  implements
    Holds<Booking, {
      holds: {
        slot: { link: "desk"; interval: { start: "slotStart"; end: "slotEnd" } };
      };
      takeOn: ["held"];
      commitOn: ["booked"];
      releaseOn: ["expired", "cancelled"];
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  slotStart: Temporal.DateTime;
  slotEnd: Temporal.DateTime;
  desk: Nullable<Relation<Desk>>;
  status: Workflow<{
    states: ["draft", "held", "booked", "expired", "cancelled"];
    initial: "draft";
    transitions: [
      { from: "draft"; to: "held" },
      { from: "held"; to: "booked" },
      { from: "held"; to: "expired" },
      { from: "held"; to: "cancelled" },
      { from: "draft"; to: "cancelled" }
    ];
  }>;
}
