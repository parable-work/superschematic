import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, key, seedRows, unique } from "@superschematic/db";

// A tier a workspace subscribes to. The rows are keyed by slug, the
// unique field every row gives; each holds text, a number, a boolean, a
// list, JSON and a null, and leaves its key and createdAt to their
// columns' defaults.
@seedRows<Tier>([
  { slug: "free", title: "Free", seats: 1, listed: true, features: ["inbox"] },
  { slug: "team", title: "Team", seats: 10, listed: true, features: ["inbox", "calendar"], limits: { storageGb: 50, exports: true } },
  { slug: "internal", title: "Staff's own", seats: 0, listed: false, features: [], note: null },
])
export abstract class Tier {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  slug: Identity.Slug;

  title: string;

  seats: Generic.Int64;

  listed: boolean;

  features: string[];

  limits: Nullable<Generic.JSON>;

  note: Nullable<string>;

  createdAt: Default<Temporal.DateTime, "now">;
}

// A reminder's lead time for a member, or for everyone: a row of no
// member is the default each member without one of their own takes. No
// unique constraint holds the rows, so the seed names the fields that
// identify one, a null member among them.
@seedRows<Reminder>([
  { member: null, kind: "*", leadMinutes: 30, quietStart: "22:00" },
  { member: null, kind: "deadline", leadMinutes: 120 },
], { key: ["member", "kind"] })
export abstract class Reminder {
  @key
  id: AutoGenerate<Identity.UUID>;

  member: Nullable<Identity.UUID>;

  kind: Default<string, "*">;

  leadMinutes: Generic.Int64;

  quietStart: Nullable<Temporal.Time>;
}

// A region, keyed by the code each row gives as the table's key.
@seedRows<Region>([
  { code: "eu", name: "Europe" },
  { code: "us", name: "United States" },
])
export abstract class Region {
  @key
  code: Identity.Slug;

  name: string;
}
