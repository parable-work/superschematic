import { Generic, Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Comments, Derived, Pool, Workflow, audience, key } from "@superschematic/db";

// A task its owner may keep to themselves or show to the principals it
// names, which moves from open to done and holds a conversation.
@audience<Task>({ owner: "owner" })
export abstract class Task implements Comments<Task, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  owner: Identity.UUID;
  status: Workflow<{
    states: ["open", "done"];
    initial: "open";
    transitions: [{ from: "open"; to: "done" }, { from: "done"; to: "open" }];
  }>;
}

// A note its author keeps to themselves unless it says otherwise, which an
// agent run reads and never writes, and whose summary a deriver holding
// notes.derive writes from its body (D146).
@audience<Note>({ owner: "author", default: "private", agents: "readOnly" })
export abstract class Note
  implements
    Derived<Note, {
      fields: { summary: { from: ["body"]; instruction: "Sum it up in one sentence."; model: "simple" } };
      permission: "notes.derive";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  author: Identity.UUID;
  body: string;
  summary: Nullable<string>;
}

// A runner work is picked onto (D104), its owner's alone unless it says
// otherwise: pick, a read of the table as a whole, answers no runner a
// caller does not see (D156).
@audience<Runner>({ owner: "owner", default: "private" })
export abstract class Runner implements Pool<Runner, { order: "rank" }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  owner: Identity.UUID;
  rank: Generic.Int64;
}
