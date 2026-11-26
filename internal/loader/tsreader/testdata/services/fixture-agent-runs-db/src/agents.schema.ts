import { Generic, Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Budget,
  Lease,
  Relation,
  Retries,
  Workflow,
  agents,
  event,
  key,
  queue,
  references,
  versioned
} from "@superschematic/db";

// The agents (D158): each a row, versioned, which a run records the
// version of. The build fills the fields.
@versioned
@agents("definitions")
export abstract class Agent {}

// The runs. The dispatcher holds a run's lease while its sandbox instance
// runs, and Retries starts an instance again after a failure, a second
// later, twice at most. A run points at the row that started it, which
// lists the run among its backlinks.
@agents("runs")
@references<AgentRun>({})
export abstract class AgentRun
  implements
    Lease<AgentRun, { onExpiry: { transition: "failed"; from: ["running", "waiting"] } }>,
    Retries<AgentRun, {
      classes: { sandbox: { attempts: 2; backoff: { firstMs: 1000; maxMs: 60000 } } };
      totalAttempts: 3;
      exhaustedState: "failed";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  status: Workflow<{
    states: ["queued", "running", "waiting", "done", "failed", "cancelled"];
    transitions: [
      { from: "queued"; to: "running" },
      { from: "running"; to: "waiting" },
      { from: "waiting"; to: "running" },
      { from: "running"; to: "queued" },
      { from: "running"; to: "done" },
      { from: "running"; to: "failed" },
      { from: "running"; to: "cancelled" },
      { from: "waiting"; to: "cancelled" },
      { from: "waiting"; to: "failed" },
      { from: "queued"; to: "cancelled" },
      { from: "queued"; to: "failed" }
    ];
  }>;
}

// The dispatcher's queue (D158).
@queue()
@agents("requests")
export abstract class AgentRunRequest {}

// Autonomy: auto, tell or ask, per agent and per operation or class.
@agents("policies")
export abstract class AutonomyPolicy {}

// What an ask holds. Its request is urgent, routed to those who may
// approve it, who approve or deny it on any channel.
@agents("approvals")
@event<AgentApproval>({ kind: "approval.requested", title: "summary", urgency: "urgent" })
export abstract class AgentApproval {
  // What an approver is shown, which titles the request's notice: a field
  // of the part's, declared so the event may name it.
  summary: string;
}

// The budget of every agent, a month at a time, which each agent's own
// spends within: the agents' total.
export abstract class AgentBudget
  implements
    Budget<AgentBudget, {
      meters: { cost: { limitField: "budgetLimit"; reset: "monthly" } };
      limitPermission: "agents.budgets";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  budgetLimit: Nullable<Generic.Int64>;
}

// One row per agent and one for every agent: the stop switch, and each
// agent's monthly budget, spent within every agent's.
@agents("controls")
@event<AgentControl>({ kind: "agent.budgetWarning", urgency: "normal" })
export abstract class AgentControl
  implements
    Budget<AgentControl, {
      meters: { cost: { limitField: "budgetLimit"; reset: "monthly"; scope: "total" } };
      limitPermission: "agents.budgets";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  budgetLimit: Nullable<Generic.Int64>;

  total: Nullable<Relation<AgentBudget, { onDelete: "SET NULL" }>>;
}
