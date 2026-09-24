import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { column, join, projection } from "@superschematic/db";

import { Channel, Preference, PreferenceScope } from "./preference.schema";

// Preference values visible to the current account, channel and user: app
// and team rows for the selected channel, and user rows only for the user
// whose session runs the query. App rows resolve by the branch or the
// released commit the caller names (either setting may be left unset), and
// one row per slot survives: user over team over app, and a branch row
// before the released commit's.
@projection<Preference>({
  pool: "app",
  name: "preferences",
  migration: "20260902120000",
  where: [
    { column: "base.account", setting: "app.account_id" },
    { column: "base.channel", setting: "app.channel_id" },
    {
      column: "base.userId",
      setting: "app.user_id",
      when: { column: "base.scope", equals: "user" }
    },
    {
      anyOf: [
        { column: "base.branch", setting: "app.branch_id", optional: true },
        { column: "base.commit", setting: "app.commit_id", optional: true }
      ],
      when: { column: "base.scope", equals: "app" }
    },
    { column: "base.archivedAt", isNull: true },
    { column: "base.hidden", equals: false },
    {
      column: "base.userId",
      notNull: true,
      when: { column: "base.scope", equals: "user" }
    }
  ],
  collapse: {
    by: ["base.slotKey"],
    order: [
      { column: "base.scope", rank: ["user", "team", "app"] },
      { column: "base.branch", direction: "asc", nulls: "last" }
    ]
  }
})
@join<Channel>("channel", { "channel.id": "base.channel" })
@join<Channel>("branch", { "branch.id": "base.branch" }, "left")
export abstract class AppPreference {
  // Stable identity of the slot the value belongs to.
  slotKey: Identity.UUID;
  handle: Identity.Slug;
  scope: PreferenceScope;
  userId: Nullable<Identity.UUID>;
  @column("base.channel")
  channelId: Identity.UUID;
  @column("channel.handle")
  channelHandle: Identity.Slug;
  @column("branch.name")
  branchName: Nullable<Identity.Name>;
  value: Generic.JSON;
  revision: Generic.Int64;
  tags: Identity.Slug[];
  @column("base.updatedAt")
  updatedAt: Nullable<Temporal.DateTime>;
}
