import { Identity } from "superscalar";
import { Assignment, AutoGenerate, Workflow, key } from "@superschematic/db";

// A problem one person works at a time (D104, D103's step 5): an
// Assignment, whose seat a caller holding problems.take takes for itself
// while it is free, and a caller holding problems.assign gives to another.
// Only its assignee starts and finishes it.
export abstract class Problem
  implements
    Assignment<Problem, { take: "problems.take"; assign: "problems.assign" }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{
    states: ["open", "working", "done"];
    transitions: [
      { from: "open"; to: "working"; by: ["seat:assignee"] },
      { from: "working"; to: "done"; by: ["seat:assignee"] }
    ];
  }>;
}

// A chore anyone may take for themselves, and no one may give to another:
// an Assignment that gives no setting.
export abstract class Chore implements Assignment<Chore, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}
