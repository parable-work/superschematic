import { Identity } from "superscalar";
import { AutoGenerate, Schedules, Workflow, key } from "@superschematic/db";

// A record of work an agent wakes on (D104). The agent sets wake-ups on it:
// after a delay, at an interval, or on a cron line in a time zone, a minute
// to thirty days apart, at most ten a record, one live name each. Each wake
// reaches the app's handler, which tells the agent and reports told,
// resumed, held or missed; a missed one is tried again five minutes on. A
// record that is done or dropped finishes its wake-ups.
export abstract class Problem
  implements
    Schedules<Problem, {
      max: 10;
      minMs: 60000;
      maxMs: 2592000000;
      kinds: ["after", "every", "cron"];
      timeZone: "America/New_York";
      outcomes: ["told", "resumed", "held", "missed"];
      retry: { outcome: "missed"; afterMs: 300000 };
      endOn: { enters: ["done", "dropped"] };
      permission: "problems.schedule";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{
    states: ["open", "working", "done", "dropped"];
    transitions: [
      { from: "open"; to: "working" },
      { from: "working"; to: "done" },
      { from: "working"; to: "open" },
      { from: "open"; to: "dropped" },
      { from: "working"; to: "dropped" }
    ];
  }>;
}
