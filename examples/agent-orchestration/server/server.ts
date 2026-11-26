/*
The wiring of work-api's TypeScript server (D158): the agent dispatcher
over work-db's repositories, the sandbox, model, credential and channel
providers the environment's config names (D159), the notifier that routes a
schedule's run to an agent and an approval's request to those who may give
it (D153), the agent routes a sandbox instance calls, and the handlers of
the API's three workers.

The imports name the packages `superschematic build-all` writes for the
example's two services, and the providers of examples/runtime-providers.
*/
import pg from 'pg';
import { createAgents, createNotifier, type AgentTool, type Agents, type Notifier } from '@superschematic/http-runtime/postgres';
import { providersFrom, SecretValue, type RuntimeInterface } from '@superschematic/http-runtime/providers';
import type { Bucket } from '@superschematic/http-runtime';
import { createOrm } from '@schemas/work-db-types/orm';
import { agentRunRequestQueue, noticeWorkQueue } from '@schemas/work-db-types/queues';
import { localSandbox } from '../../runtime-providers/providers/local-sandbox/src/index.ts';
import { stubModel } from '../../runtime-providers/providers/stub-model/src/index.ts';
import { smsRelayA } from '../../runtime-providers/providers/sms-relay-a/src/index.ts';
import { vaultBroker } from '../../runtime-providers/providers/vault-broker/src/index.ts';

/** The provider slots of WorkApiConfig, as its @providers declares them. */
const SLOTS: Readonly<Record<string, RuntimeInterface>> = { SANDBOX: 'sandbox', MODEL: 'model', CREDENTIALS: 'credentials', SMS: 'channel' };

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

export interface Wiring {
  readonly agents: Agents;
  readonly notifier: Notifier;
  /** The agent routes, at <endpoint>/turn, /mcp, /credential and /finish, which only a run's token calls. */
  readonly routes: (request: Request) => Promise<Response | null>;
}

/**
 * Builds the orchestration. endpoint is where a sandbox instance reaches
 * this server's agent routes; transcripts is the bucket a run's redacted
 * transcript is kept in once it ends, for 30 days.
 */
export function wire(pool: pg.Pool, config: Record<string, unknown>, endpoint: string, transcripts: Bucket): Wiring {
  const orm = createOrm(pool);
  const providers = providersFrom({ slots: SLOTS, config, definitions: [localSandbox, stubModel, vaultBroker(), smsRelayA()] });
  let agents: Agents | undefined;
  const notifier = createNotifier({
    orm,
    dispatch: noticeWorkQueue,
    providers,
    // "approve 12" or "deny 12" on any channel decides approval 12, as the
    // replier, held to the approval's permission and the channel's trust.
    replies: {
      'approval.requested': {
        approve: { permission: 'agents.approve', describe: 'Approved', run: ctx => agents!.replyActions.approve.run(ctx) },
        deny: { permission: 'agents.approve', describe: 'Denied', run: ctx => agents!.replyActions.deny.run(ctx) },
      },
    },
    // A route whose recipients name { agent: "reviewer" } starts a run.
    agents: { request: (tx, input) => agents!.notifierAgents.request(tx, input) },
  });
  agents = createAgents({
    orm,
    requests: agentRunRequestQueue,
    endpoint,
    providers,
    notifier,
    tools,
    search: true,
    transcripts,
    // What each instance needs beside its run: the credential broker's
    // settings, so it resolves its run's references itself. The run's
    // record and transcript hold the references alone.
    sandboxSpec: () => ({
      env: { CREDENTIALS_URL: String(config.CREDENTIALS_URL ?? ''), CREDENTIALS_VAULTS: String(config.CREDENTIALS_VAULTS ?? '') },
      secrets: config.CREDENTIALS_TOKEN instanceof SecretValue ? { CREDENTIALS_TOKEN: config.CREDENTIALS_TOKEN as SecretValue<string> } : {},
    }),
    // A run's question nobody answered by its deadline becomes a task.
    followUp: async (tx, { row, question }) => {
      const made = await tx.task.createOne({ title: `Follow up: ${question}`, details: `asked on ${row.key}`, state: 'open', createdAt: new Date() });
      return { table: 'Task', key: String(made.id) };
    },
    // A task assigned to a paused agent goes back to the queue.
    onReturn: async (tx, { row }) => {
      await tx.task.updateOne(row.key, { state: 'queued', assignee: null });
    },
  });
  return { agents, notifier, routes: agents.routes() };
}

/**
 * The handlers of the API's workers: Dispatch runs the dispatcher, Notify
 * the notifier, and TaskDeadlines hands each schedule's run to the
 * notifier, whose route names the agent. On Workers each drains at a Cron
 * firing and a message due within the minute wakes it (D92, D151).
 */
export function workers(pool: pg.Pool, wiring: Wiring) {
  return {
    Dispatch: wiring.agents.workerQueue(pool),
    Notify: wiring.notifier.workerQueue(pool),
    TaskDeadlines: {
      async schedulesRun(tx: unknown, event: { readonly key: string; readonly kind: string; readonly payload?: unknown }) {
        await wiring.notifier.fromBehaviorEvent(tx, 'Task', 'schedules', event);
      },
    },
  };
}
