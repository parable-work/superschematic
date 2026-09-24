import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key, unique } from "@superschematic/db";

export abstract class Auditable {
  @unique
  @key
  id: AutoGenerate<Identity.UUID>;

  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

export enum PreferenceScope {
  App = "app",
  Team = "team",
  User = "user"
}

// A release channel of an app: the line a preference row resolves against.
export abstract class Channel extends Auditable {
  account: Identity.UUID;
  name: Identity.Name;
  handle: Identity.Slug;
}

// One stored preference value for a slot, scoped to an app, a team, or a
// user.
@index<Preference>(["account", "slotKey"])
export abstract class Preference extends Auditable {
  account: Identity.UUID;
  channel: Relation<Channel>;
  branch: Nullable<Relation<Channel>>;
  // A released value's commit; a row carries a branch or a commit, never
  // both.
  commit: Nullable<Identity.UUID>;
  scope: PreferenceScope;
  userId: Nullable<Identity.UUID>;
  slotKey: Identity.UUID;
  handle: Identity.Slug;
  value: Generic.JSON;
  revision: Default<Generic.Int64, 1>;
  tags: Identity.Slug[];
  // Set when the row is retired; retired rows leave the projection.
  archivedAt: Nullable<Temporal.DateTime>;
  // Hidden rows are kept for audit but never served.
  hidden: Default<boolean, false>;
}
