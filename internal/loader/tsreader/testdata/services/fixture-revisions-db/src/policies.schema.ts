import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Revisions, key } from "@superschematic/db";

// A policy whose every change is kept (D16, D103's step 4): revision 1 at
// its create and the next at each change of its own fields, and a change a
// caller proposes waits for a reviewer who holds policies.review, whose
// approval applies it through every rule an update meets.
export abstract class Policy implements Revisions<Policy, { review: { permission: "policies.review" } }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  body: Nullable<string>;
  updatedAt: Temporal.DateTime;
}

// A note whose history is kept, with no review step.
export abstract class Note implements Revisions<Note, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  text: string;
}
