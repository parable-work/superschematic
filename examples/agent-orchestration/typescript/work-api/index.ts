/*
The implementation of work-api (D158, D183). The generated server wires the
runtime pieces work-db declares into the API's Deps itself: the providers
the environment's config names (Deps.providers, D159), the notifier
(Deps.notifier, D153), whose route that names an agent starts its run and
whose "approve 12" decides approval 12, the inbound path a customer's text
takes (Deps.inbound, D157), and the agent dispatcher (Deps.agents) with what
createAgentOptions answers here. It mounts the agent routes a run's sandbox
instance calls and the approvals' approve and deny, the SMS relay's
callbacks, the inbox and search across tables; it hands each message of the
dispatcher's queues (Dispatch, InboundRuns), the notifier's (Notify) and the
indexing queue (Indexing) to its runtime piece; and it runs a grant's token
as its holder. What is left to the app is what only it knows: the tools an
agent may call, what a sandbox instance needs beside its run, the
follow-up of a question nobody answered, and the operations and the
workers of its own.

The imports name the packages `superschematic build-all` writes for the
example's services.
*/
import { HttpProblem, notFound, type Principal } from '@superschematic/http-runtime';
import type { AgentTool } from '@superschematic/http-runtime/postgres';
import { SecretValue } from '@superschematic/http-runtime/providers';
import type { AgentOptionsConstructor, AuthenticatorFactory, Constructor, Deps, WorkImplementation, WorkersConstructor } from '@schemas/work-api-api';

/** Where a run's sandbox instance reaches the agent routes when the config names nowhere: the server's default port. */
const DEFAULT_ENDPOINT = 'http://localhost:8080/api/agents';

/**
 * The app's tools, which an agent's role and its definition's tools allow:
 * each says its class, which the autonomy policy reads, and runs in a
 * transaction as the run's principal, so its reads and writes keep to the
 * run's targets and the rows its role reaches from them.
 */
const tools: Record<string, AgentTool> = {
  read_task: {
    description: 'Reads a task by its key.',
    inputSchema: { type: 'object', required: ['key'], properties: { key: { type: 'string' } } },
    class: 'read',
    async run({ tx, args }) {
      return tx.task.getOne(String(args.key));
    },
  },
  pay_invoice: {
    description: 'Pays an invoice an amount.',
    inputSchema: { type: 'object', required: ['invoice', 'amount'], properties: { invoice: { type: 'string' }, amount: { type: 'integer' } } },
    // An agent only ever asks to move money: the approval holds these exact parameters.
    class: 'moneyMovement',
    permission: 'invoices.pay',
    async run({ tx, args }) {
      return tx.invoice.updateOne(String(args.invoice), { paid: Number(args.amount) });
    },
  },
};

/**
 * The dispatcher's options beside what the generated entrypoint fills
 * (D183): its ORM and queue, the providers, the notifier and the inbound
 * path. Where a sandbox instance reaches the agent routes, the app's tools
 * and search across tables. A run's transcript stays on its run row; a
 * bucket (D54) passed as transcripts would keep it once the run ends, for
 * 30 days.
 */
export const createAgentOptions: AgentOptionsConstructor = deps => {
  const config = deps.config;
  return {
    endpoint: config.AGENT_ENDPOINT ?? DEFAULT_ENDPOINT,
    tools,
    search: true,
    // What each instance needs beside its run: the credential broker's
    // settings, so it resolves its run's references itself. The run's
    // record and transcript hold the references alone.
    sandboxSpec: () => {
      const secrets: Record<string, SecretValue<string>> = {};
      if (config.CREDENTIALS_TOKEN instanceof SecretValue) secrets.CREDENTIALS_TOKEN = config.CREDENTIALS_TOKEN as SecretValue<string>;
      return { env: { CREDENTIALS_URL: String(config.CREDENTIALS_URL ?? ''), CREDENTIALS_VAULTS: String(config.CREDENTIALS_VAULTS ?? '') }, secrets };
    },
    // A run's question nobody answered by its deadline becomes a task.
    followUp: async (tx, { row, question }) => {
      const made = await tx.task.createOne({ title: `Follow up: ${question}`, details: `asked on ${row.key}`, state: 'open', createdAt: new Date() });
      return { table: 'Task', key: String(made.id) };
    },
    // A task assigned to a paused agent goes back to the queue.
    onReturn: async (tx, { row }) => {
      await tx.task.updateOne(row.key, { state: 'queued', assignee: null });
    },
  };
};

/**
 * The API's callers: a run's token, which resolves to the run as an agent
 * principal acting for whom it acts for, its permissions and reach
 * narrowed (D158, D183). The generated server runs a customer's grant's
 * token as the customer around it (D156). The routes are an agent's, so no
 * other caller is established here.
 */
export const authenticate: AuthenticatorFactory = deps => deps.agents.authenticator();

export const create: Constructor = deps => ({ work: workImplementation(deps) });

/**
 * The worker the app runs itself: TaskDeadlines' schedule runs, which go to
 * the notifier, whose route names the agent. Dispatch, InboundRuns, Notify
 * and Indexing are the runtime pieces', which the generated workers hand
 * each message to.
 */
export const createWorkers: WorkersConstructor = deps => ({
  // The schedule's run, an event of kind run: the route that names the
  // agent starts its run.
  async taskDeadlinesSchedulesRun(event, tx) {
    await deps.notifier.fromBehaviorEvent(tx, 'Task', 'schedules', { kind: 'run', key: event.key, payload: event.payload, at: Number(event.at) });
  },
});

/**
 * An agent's call of a route is held to its run's autonomy as its MCP
 * endpoint's is (D158): the gate answers what the call does instead, or
 * nothing to run it. Any other principal runs.
 */
async function held(deps: Deps, principal: Principal | null, operation: string, args: Readonly<Record<string, unknown>>, cls: 'read' | 'moneyMovement'): Promise<void> {
  if (!principal || principal.kind !== 'agent') return;
  const gated = await deps.agents.gate(principal, operation, args, { class: cls });
  if (!gated.run) throw new HttpProblem(gated.status, gated.detail, { code: gated.code, ...(gated.approval ? { extensions: { approval: gated.approval } } : {}) });
}

function workImplementation(deps: Deps): WorkImplementation {
  return {
    async getTask({ id }, ctx) {
      await held(deps, ctx.principal, 'getTask', { id }, 'read');
      const row = await deps.orm.task.findOne({ id: { eq: id } });
      if (!row) throw notFound(`no task ${id}`);
      return { id: row.id, title: row.title, state: row.state };
    },
    async payInvoice({ input }, ctx) {
      // An agent only ever asks to move money: the approval holds the exact invoice and amount.
      await held(deps, ctx.principal, 'payInvoice', { invoice: input.invoice, amount: input.amount }, 'moneyMovement');
      const row = await deps.orm.invoice.updateOne(input.invoice, { paid: input.amount });
      return { id: row.id, amount: row.amount, paid: row.paid };
    },
  };
}
