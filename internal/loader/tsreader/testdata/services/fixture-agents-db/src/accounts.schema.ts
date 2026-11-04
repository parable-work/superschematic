import { Identity } from "superscalar";
import { AutoGenerate, Budget, Relation, Workflow, key } from "@superschematic/db";

// A workspace whose accounts' usage rolls up into its own caps (D104): sixty
// questions an hour, and two million tokens over any day.
export abstract class Workspace
  implements
    Budget<Workspace, {
      meters: {
        questions: { limit: 60; reset: "hourly" };
        tokens: { limit: 2000000; reset: "daily"; window: "rolling" };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

// An account an agent runs on (D104): forty questions an hour and a million
// tokens over any day, each charged to its workspace too. The provider's own
// count of its tokens, read every few minutes, replaces what the account
// counted until the provider's period rolls over (observe, for callers who
// hold accounts.observe). Usage that takes it past a cap moves it out, until
// a person moves it back.
export abstract class Account
  implements
    Budget<Account, {
      meters: {
        questions: { limit: 40; reset: "hourly"; scope: "workspace" };
        tokens: { limit: 1000000; reset: "daily"; window: "rolling"; scope: "workspace" };
      };
      limitPermission: "accounts.limits";
      observePermission: "accounts.observe";
      escalate: { transition: "out"; from: ["open"] };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  workspace: Relation<Workspace>;
  status: Workflow<{
    states: ["open", "out"];
    transitions: [{ from: "open"; to: "out" }, { from: "out"; to: "open" }];
  }>;
}
