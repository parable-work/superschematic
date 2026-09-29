import { Identity, Temporal } from "superscalar";
import { AutoGenerate, JsonField, jsonField, key } from "@superschematic/db";

// A tasting whose required columns hold temporal values. A required
// temporal scalar defaults to the current date or time; a required list of
// one defaults to an empty array; a list of lists, a map or a JSON field
// holding one has no default.
export abstract class Tasting {
  @key
  id: AutoGenerate<Identity.UUID>;

  tastedAt: Temporal.DateTime;
  tastedOn: Temporal.Date;
  pouredAt: Temporal.Time;

  retastedAt: Temporal.DateTime[];
  retastedOn: Temporal.Date[];
  repouredAt: Temporal.Time[];

  // Restock times; the database starts the list empty.
  restockedAt: AutoGenerate<Temporal.DateTime[]>;

  // Pour times, one inner list per flight.
  flights: Temporal.DateTime[][];

  // The time each bottle was opened, by bottle label.
  openedAt: Record<string, Temporal.DateTime>;

  @jsonField
  bottledAt: JsonField<Temporal.DateTime>;
}
