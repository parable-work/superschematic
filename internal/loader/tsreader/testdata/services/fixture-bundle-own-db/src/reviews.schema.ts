import { Identity } from "superscalar";
import { AutoGenerate, Bundle, StateRules, Workflow, key, type FieldOf } from "@superschematic/db";

// A bundle the project declares (D103, step 5): a draft that a reviewer
// reviews, frozen while it is in review but for the reviewer the setting
// names, who alone approves it. TypeScript alone reads it: the data forms
// carry its parts.
type Reviewed<T, S extends { reviewer: FieldOf<T>; approvePermission?: string }> = Bundle<
  [StateRules<T, { states: { review: { frozen: "all"; by: [S["reviewer"]] } } }>],
  {
    status: Workflow<{
      states: ["draft", "review", "approved"];
      transitions: [
        { from: "draft"; to: "review" },
        { from: "review"; to: "draft"; by: [S["reviewer"]] },
        { from: "review"; to: "approved"; by: [S["reviewer"]]; permission: S["approvePermission"] }
      ];
    }>;
  }
>;

// A proposal its reviewer approves, holding proposals.approve.
export abstract class Proposal
  implements
    Reviewed<Proposal, { reviewer: "reviewer"; approvePermission: "proposals.approve" }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  reviewer: Identity.UUID;
}

// A note its reviewer approves, with no permission beside.
export abstract class Note implements Reviewed<Note, { reviewer: "checkedBy" }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  text: string;
  checkedBy: Identity.UUID;
}
