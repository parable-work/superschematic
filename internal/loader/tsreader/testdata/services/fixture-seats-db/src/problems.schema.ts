import { Identity } from "superscalar";
import { AutoGenerate, Seats, StateRules, Workflow, key } from "@superschematic/db";

// A record of work (D104). It has one accountable person, whom the next to
// take the seat displaces; any number of responsible agents, the record's
// own agent always among them; and at most five handles, which any caller
// may claim and only the agent that claimed one, or a person who may
// release another's, releases. Only a responsible subject starts and
// submits the record, only the accountable person closes it or sends it
// back, and in review only the accountable person edits its title.
export abstract class Problem
  implements
    Seats<Problem, {
      seats: {
        accountable: { max: 1; onFull: "displace"; take: "problems.own"; assign: "problems.assign" };
        responsible: { fromField: "agent"; take: "problems.work"; assign: "problems.assign" };
        handles: { max: 5; assign: "problems.release" };
      };
    }>,
    StateRules<Problem, {
      states: { review: { frozen: ["title"]; by: ["seat:accountable"] } };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  agent?: Identity.UUID;
  status: Workflow<{
    states: ["open", "working", "review", "done"];
    transitions: [
      { from: "open"; to: "working"; by: ["seat:responsible"] },
      { from: "working"; to: "review"; by: ["seat:responsible"] },
      { from: "review"; to: "done"; by: ["seat:accountable"] },
      { from: "review"; to: "working"; by: ["seat:accountable"] }
    ];
    overridePermission: "problems.override";
  }>;
}
