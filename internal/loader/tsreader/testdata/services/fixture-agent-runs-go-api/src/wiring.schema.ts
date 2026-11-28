import { Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import { Authenticated, HttpMethod, rest, source, tableOperation, worker } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { AgentRunRequest, NoticeWork, RunRequest, SearchIndexing, Task } from "@schemas/fixture-agent-runs-db";

// The API's config (D159): the SMS relay the notifier and the inbound path
// send and receive on, and the sandbox and model an agent's run takes.
// AGENT_ENDPOINT is where a run's sandbox instance reaches this server's
// agent routes, which the implementation's NewWiring names.
@envVars
@providers({ SMS: "channel", SANDBOX: "sandbox", MODEL: "model" })
export abstract class FixtureAgentRunsGoConfig {
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;
  SMS_SENDER: Nullable<string>;

  SANDBOX: Nullable<string>;
  SANDBOX_WORKDIR: Nullable<string>;

  MODEL: Nullable<string>;

  AGENT_ENDPOINT: Nullable<string>;
}

// A task as the API answers it.
@source(Task)
export abstract class TaskView {
  id: Identity.UUID;
  title: string;
  state: string;
}

export class Tasks extends Authenticated {
  // One task, read as its caller: a grant's holder reaches only the tasks
  // its grant does.
  @rest(HttpMethod.GET, "tasks/{id}")
  @tableOperation()
  getTask(id: Identity.UUID): TaskView {
    throw new Error("schema declaration only");
  }
}

// The notifier's queue (D190): the generated worker hands each message to
// Deps.Notifier, and the implementation has no method for it.
@worker({ queue: NoticeWork, concurrency: 4 })
export abstract class Notify {}

// The agent dispatcher's queue (D192), Deps.Agents'.
@worker({ queue: AgentRunRequest, concurrency: 4 })
export abstract class Dispatch {}

// The run requests a party's inbound message makes (D157), which the
// generated worker hands the dispatcher as they are.
@worker({ queue: RunRequest })
export abstract class InboundRuns {}

// The search index's queue (D155, D187): each message's row indexed again
// by crosstable.IndexSearch.
@worker({ queue: SearchIndexing })
export abstract class IndexSearch {}
