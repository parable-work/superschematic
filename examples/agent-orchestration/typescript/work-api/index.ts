/*
The implementation of work-api (D158, D183). The generated server builds the
agent dispatcher into the API's Deps (Deps.agents) with what
createAgentOptions answers here, mounts the agent routes a run's sandbox
instance calls, and hands each message of the dispatcher's queues, Dispatch's
AgentRunRequest and InboundRuns' RunRequest (D157), to the dispatcher. What
is left to the app is what only it knows: the providers the environment's
config names (D159), the notifier that routes a schedule's run to an agent
and an approval's request to those who may give it (D153), the inbound path
a customer's text takes (D157), the tools an agent may call, and the
operations and the workers of its own.

The imports name the packages `superschematic build-all` writes for the
example's services, and the providers of examples/runtime-providers, whose
scripts/link.sh puts the HTTP runtime beside them.
*/
import { HttpProblem, notFound, type Principal } from '@superschematic/http-runtime';
import { createInbound, createNotifier, type AgentTool, type Agents, type Inbound, type Notifier } from '@superschematic/http-runtime/postgres';
import { providersFrom, SecretValue, type Providers, type RuntimeInterface } from '@superschematic/http-runtime/providers';
import type { AgentOptionsConstructor, AuthenticatorFactory, Constructor, Deps, WorkImplementation, WorkersConstructor } from '@schemas/work-api-api';
import { noticeWorkQueue, runRequestQueue } from '@schemas/work-db-types/queues';
import { localSandbox } from '../../../runtime-providers/providers/local-sandbox/src/index.ts';
import { stubModel } from '../../../runtime-providers/providers/stub-model/src/index.ts';
import { smsRelayA } from '../../../runtime-providers/providers/sms-relay-a/src/index.ts';
import { vaultBroker } from '../../../runtime-providers/providers/vault-broker/src/index.ts';

/** The provider slots of WorkApiConfig, as its @providers declares them. */
const SLOTS: Readonly<Record<string, RuntimeInterface>> = { SANDBOX: 'sandbox', MODEL: 'model', CREDENTIALS: 'credentials', SMS: 'channel' };

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
 * What the app wires once per process, over the API's pool: the notifier
 * and the inbound path, which the dispatcher's options name and the
 * operations and the workers use too. The dispatcher is Deps.agents, which
 * the generated entrypoint builds once createAgentOptions has answered; the
 * notifier's reply actions and agent recipients, and the inbound path's
 * approval hook, reach it through agentsOf once create or createWorkers has
 * been handed it.
 */
interface Wiring {
  readonly providers: Providers;
  readonly notifier: Notifier;
  readonly inbound: Inbound;
  agents?: Agents;
}

const wirings = new WeakMap<object, Wiring>();

function wiringOf(deps: Omit<Deps, 'agents'>): Wiring {
  const existing = wirings.get(deps.db);
  if (existing) return existing;
  const config = deps.config as unknown as Record<string, unknown>;
  const providers = providersFrom({ slots: SLOTS, config, definitions: [localSandbox, stubModel, vaultBroker(), smsRelayA()] });
  const agentsOf = (): Agents => {
    if (!wiring.agents) throw new Error("work-api: the dispatcher is not built yet: the entrypoint hands Deps to create or createWorkers before a reply or a send needs it");
    return wiring.agents;
  };
  const notifier = createNotifier({
    orm: deps.orm,
    dispatch: noticeWorkQueue,
    providers,
    // "approve 12" or "deny 12" on any channel decides approval 12, as the
    // replier, held to the approval's permission and the channel's trust.
    replies: {
      'approval.requested': {
        approve: { permission: 'agents.approve', describe: 'Approved', run: ctx => agentsOf().replyActions.approve.run(ctx) },
        deny: { permission: 'agents.approve', describe: 'Denied', run: ctx => agentsOf().replyActions.deny.run(ctx) },
      },
    },
    // A route whose recipients name { agent: "reviewer" } starts a run.
    agents: { request: (tx, input) => agentsOf().notifierAgents.request(tx, input) },
  });
  // A send to a party as an agent run is send_message, class
  // messageParty: it asks unless a policy row says otherwise, and the
  // approval releases the held message.
  const inbound = createInbound({ orm: deps.orm, notifier, runs: runRequestQueue, providers, approve: request => agentsOf().sendApproval(request) });
  const wiring: Wiring = { providers, notifier, inbound };
  wirings.set(deps.db, wiring);
  return wiring;
}

/** Records the dispatcher the entrypoint built, for the wiring that reaches it. */
function withDispatcher(deps: Deps): Wiring {
  const wiring = wiringOf(deps);
  wiring.agents = deps.agents;
  return wiring;
}

/**
 * The dispatcher's options beside its ORM and its queue, which the
 * generated entrypoint fills (D183): where a sandbox instance reaches the
 * agent routes, the providers the environment's config names, the
 * notifier and the inbound path, the app's tools and search across tables.
 * A run's transcript stays on its run row; a bucket (D54) passed as
 * transcripts would keep it once the run ends, for 30 days.
 */
export const createAgentOptions: AgentOptionsConstructor = deps => {
  const { providers, notifier, inbound } = wiringOf(deps);
  const config = deps.config;
  return {
    endpoint: config.AGENT_ENDPOINT ?? DEFAULT_ENDPOINT,
    providers,
    notifier,
    // An inbound message's run reads it through the inbound path, and the
    // message_party tool sends through it.
    inbound,
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
 * narrowed (D158, D183). The routes are an agent's, so no other caller is
 * established here.
 */
export const authenticate: AuthenticatorFactory = deps => deps.agents.authenticator();

export const create: Constructor = deps => {
  withDispatcher(deps);
  return { work: workImplementation(deps) };
};

/**
 * The workers the app runs itself: Notify, the notifier's dispatch queue,
 * and TaskDeadlines' schedule runs, which go to the notifier, whose route
 * names the agent. Dispatch and InboundRuns are the dispatcher's, which the
 * generated workers hand each message to.
 */
export const createWorkers: WorkersConstructor = deps => {
  const { notifier } = withDispatcher(deps);
  return {
    // The queue's message as the notifier wrote it, whose step the
    // database's types read as text.
    async notify(message) {
      await notifier.handle(message as Parameters<Notifier['handle']>[0]);
    },
    // The schedule's run, an event of kind run: the route that names the
    // agent starts its run.
    async taskDeadlinesSchedulesRun(event, tx) {
      await notifier.fromBehaviorEvent(tx, 'Task', 'schedules', { kind: 'run', key: event.key, payload: event.payload, at: Number(event.at) });
    },
  };
};

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
