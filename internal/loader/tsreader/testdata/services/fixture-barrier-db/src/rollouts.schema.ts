import { Identity } from "superscalar";
import { AutoGenerate, Barrier, HasMany, Relation, Workflow, key } from "@superschematic/db";

// A rollout that waits for its agents (D104, D103's step 5): a Barrier,
// released once every agent that points at it is ready, its deadline
// trying the release five minutes after it begins waiting, and a person
// holding rollouts.release giving the go-ahead.
export abstract class Rollout
  implements
    Barrier<Rollout, {
      rows: "Agent";
      link: "rollout";
      timeoutMs: 300000;
      releasePermission: "rollouts.release";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  agents: HasMany<Agent>;
}

// An agent a rollout waits for: ready once it says so, gone when it stops
// answering, which a rollout does not count as ready.
export abstract class Agent {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  rollout: Relation<Rollout>;
  status: Workflow<{
    states: ["running", "ready", "gone"];
    transitions: [{ from: "running"; to: "ready" }, { from: "running"; to: "gone" }];
    outcomes: { gone: "neutral" };
  }>;
}
