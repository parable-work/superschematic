import { Identity } from "superscalar";
import { column, projection } from "@superschematic/db";

import { Note } from "./note.schema";

// The notes of the account the session names: the one view the capture
// functions list, since it reads app.account_id and nothing else.
@projection<Note>({
  pool: "scope",
  name: "notes",
  migration: "20261126120000",
  where: [{ column: "base.account", setting: "app.account_id" }]
})
export abstract class ScopeNote {
  id: Identity.UUID;
  title: string;
}

// The notes of one team of the account the session names. The team may be
// left unset, which serves no note, so the view also reads an optional
// setting.
@projection<Note>({
  pool: "scope",
  name: "team_notes",
  migration: "20261126120100",
  where: [
    { column: "base.account", setting: "app.account_id" },
    { column: "base.team", setting: "app.team_id", optional: true }
  ]
})
export abstract class ScopeTeamNote {
  id: Identity.UUID;
  title: string;
}

// The notes of the team the session names, whatever their account: a view
// scoped by another setting.
@projection<Note>({
  pool: "team",
  name: "notes",
  migration: "20261126120200",
  where: [{ column: "base.team", setting: "app.team_id" }]
})
export abstract class TeamNote {
  id: Identity.UUID;
  @column("base.account")
  accountId: Identity.UUID;
  title: string;
}
