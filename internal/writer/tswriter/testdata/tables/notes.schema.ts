import { AutoGenerate, Comments, Revisions, Workflow, key } from "@superschematic/db";
import { Nullable, Validate } from "@superschematic/schema";
import { Identity } from "superscalar";

// A note that moves from draft through review to published, collects comments, and keeps its revisions. A change to it can be proposed for an editor to approve.
export abstract class Note implements Comments<Note, {}>, Revisions<Note, { review: { permission: "notes.review" } }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  // The note's title.
  title: Validate<string, { maxLength: 200 }>;

  // The note's text.
  body: Nullable<string>;

  // Words to find the note by.
  tags: Nullable<string[]>;

  status: Workflow<{ states: ["draft", "review", "published", "archived"]; transitions: [{ from: "draft"; to: "review" }, { from: "review"; to: "draft" }, { from: "review"; permission: "notes.publish"; to: "published" }, { from: "published"; permission: "notes.publish"; to: "archived" }] }>;
}
