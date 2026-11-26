import { Generic, Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import { Authenticated, HttpMethod, autonomy, behaviorWorker, requirePermission, rest, worker } from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { AgentRunRequest, NoticeWork, RunRequest, Task } from "@schemas/work-db";

// The API's config (D159). An agent's run needs a sandbox and a model
// (D158), so an environment that fills no SANDBOX or no MODEL slot is
// refused; CREDENTIALS is the broker each instance resolves its credential
// references through, and SMS the channel an approval is asked for on.
@envVars
@providers({ SANDBOX: "sandbox", MODEL: "model", CREDENTIALS: "credentials", SMS: "channel" })
export abstract class WorkApiConfig {
  SANDBOX: Nullable<string>;
  SANDBOX_WORKDIR: Nullable<string>;

  MODEL: Nullable<string>;
  MODEL_MODEL: Nullable<string>;

  CREDENTIALS: Nullable<string>;
  CREDENTIALS_URL: Nullable<string>;
  CREDENTIALS_TOKEN: Nullable<Secret<string>>;
  CREDENTIALS_VAULTS: Nullable<string>;

  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;
  SMS_SENDER: Nullable<string>;

  // The confirmation key of the agent routes' MCP endpoint is the run's
  // token: a grant (D156). Nothing else to bind.
}

export abstract class InvoicePayment {
  invoice: Identity.UUID;
  amount: Generic.Int64;
}

export abstract class InvoiceView {
  id: Identity.UUID;
  amount: Generic.Int64;
  paid: Generic.Int64;
}

export abstract class TaskView {
  id: Identity.UUID;
  title: string;
  state: string;
}

// What an agent may do through the API. Each operation says what it is to
// an agent's autonomy policy (D158): reading a task runs; paying an
// invoice moves money, which an agent only ever asks to do, and the build
// refuses any other mode for it.
export class Work extends Authenticated {
  @rest(HttpMethod.GET, "tasks/{id}")
  @requirePermission(["tasks.read"])
  @autonomy("read")
  getTask(id: Identity.UUID): TaskView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "invoices/pay")
  @requirePermission(["invoices.pay"])
  @autonomy("moneyMovement")
  payInvoice(input: InvoicePayment): InvoiceView {
    throw new Error("schema declaration only");
  }
}

// The agent dispatcher (D158): claims each run request, writes its run,
// mints its token and starts its sandbox instance; watches it until it
// ends; and makes the follow-up of a question nobody answered. A @worker of
// a D80 queue, so on Workers it drains at each Cron firing and a request
// due within the minute wakes it (D92, D151).
@worker({ queue: AgentRunRequest, concurrency: 4 })
export abstract class Dispatch {}

// The run requests a customer's inbound message makes (D157), which the
// dispatcher takes as they are.
@worker({ queue: RunRequest, concurrency: 4 })
export abstract class InboundRuns {}

// The notifier (D153), which routes a schedule's run to an agent and an
// approval's request to those who may give it.
@worker({ queue: NoticeWork, concurrency: 4 })
export abstract class Notify {}

// Fires each task's schedules and hands their runs to the notifier.
@behaviorWorker(Task, { permissions: [] })
export abstract class TaskDeadlines {}
