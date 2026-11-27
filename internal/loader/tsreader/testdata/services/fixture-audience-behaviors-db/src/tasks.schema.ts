import { Identity } from "superscalar";
import { AutoGenerate, Comments, Workflow, audience, key } from "@superschematic/db";

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
