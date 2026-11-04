import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Constants, StateRules, Workflow, key, unique } from "@superschematic/db";

// A note a person drafts and publishes, kept by its slug (D104). It keeps
// no sub-table, so it has upserts, and an upsert's update of the note its
// slug holds runs through the core as an update does: its author stays who
// its create named, and pinned and views only go up, unless the caller
// holds notes.admin; while it is a draft only its creator edits its body.
// A deleted note is kept, deletedAt stamped, and an upsert of its slug
// finds none.
export abstract class Note
  implements
    Constants<Note, {
      fields: ["author"];
      oneWay: { pinned: "up"; views: "up" };
      permission: "notes.admin";
    }>,
    StateRules<Note, {
      states: {
        draft: { frozen: ["body"]; by: ["createdBy"] };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  @unique
  slug: string;
  title: string;
  body: string;
  author: Identity.UUID;
  pinned: boolean;
  views: number;
  createdBy: Identity.UUID;
  deletedAt: Nullable<Temporal.DateTime>;
  status: Workflow<{
    states: ["draft", "published"];
    transitions: [{ from: "draft"; to: "published" }];
  }>;
}
