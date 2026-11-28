// A reply interpreter other than keywords (#602, D153, D158), end to end on
// fixture-agent-runs-db: a reply on a verified channel that no keyword
// reads goes to an agent run, which acts for its sender with the sender's
// permissions, answers it through the notifier's reply tool, and the
// action is repeated back with its undo. TestReplyInterpreterEndToEnd runs
// it on Postgres and TestReplyInterpreterEndToEndOnSQLite on SQLite, with
// the runtime-providers example's stubs: the local-process sandbox, whose
// instances run agent_runner.ts under bun, the model stub, whose answers
// follow from the turn ("call <tool> <json>" calls the tool), and the SMS
// relay the replies come in and go back on.
import { afterEach, describe, expect, test } from 'bun:test';
import { createAgents, createNotifier, type Agents, type AgentTool, type Notifier, type ReplyAction } from '@superschematic/http-runtime/postgres';
import { providersFrom, SecretValue, type Providers, type SandboxProvider } from '@superschematic/http-runtime/providers';
import type { FixtureAgentRunsDbOrm } from './orm';
import { agentRunRequestQueue, noticeWorkQueue } from './queues';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';
import { localSandbox } from './local-sandbox';
import { stubModel } from './stub-model';
import { smsRelayA } from './sms-relay-a';

const RELAY_TOKEN = 'relay-token';
const BUN = process.env.AGENT_RUNNER_BUN || process.execPath;
const PHONE = { boss: '+15550100031', ann: '+15550100032', entered: '+15550100033' };

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

interface World {
  db: FeatureDatabase;
  orm: FixtureAgentRunsDbOrm;
  sys: FixtureAgentRunsDbOrm;
  agents: Agents;
  notifier: Notifier;
  sandbox: SandboxProvider;
  instances: string[];
  sms: { to: string; text: string }[];
  stopApp: () => void;
  ids: Record<string, string>;
}

/** The app's one other tool: a task's details, which a run reaches only on its target. */
const tools: Record<string, AgentTool> = {
  note_task: {
    description: "Writes a task's details.",
    inputSchema: { type: 'object', required: ['key', 'details'], properties: { key: { type: 'string' }, details: { type: 'string' } } },
    class: 'internalWrite',
    async run({ tx, args }) {
      const row = await tx.task.updateOne(String(args.key), { details: String(args.details) });
      return { id: row.id, details: row.details };
    },
  },
};

/** The reply a review's notice takes: close the task, which undo opens again. */
const close: ReplyAction = {
  permission: 'tasks.write',
  describe: 'Closed',
  async run({ tx, notice }) {
    const before = await tx.task.findOne({ id: { eq: String(notice.subjectKey) } });
    await tx.task.updateOne(String(notice.subjectKey), { state: 'closed' });
    return { state: before?.state ?? 'open' };
  },
  async undo({ tx, notice }, data) {
    await tx.task.updateOne(String(notice.subjectKey), { state: (data as { state: string }).state });
  },
};

async function world(): Promise<World> {
  const db = await featureDatabase('replies');
  const orm = (await ormEntry<FixtureAgentRunsDbOrm>()).createOrm(db.source);
  const sys = orm.asSystem();
  const sms: { to: string; text: string }[] = [];
  const relayFetch = (async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input));
    if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    sms.push({ to: body.to, text: body.text });
    return Response.json({ id: `sms-${sms.length}`, status: 'accepted' });
  }) as typeof globalThis.fetch;
  const base = providersFrom({
    slots: { SANDBOX: 'sandbox', MODEL: 'model', SMS: 'channel' },
    config: { SANDBOX: 'local-sandbox', SANDBOX_WORKDIR: import.meta.dir, MODEL: 'stub-model', SMS: 'sms-relay-a', SMS_URL: 'https://sms.test', SMS_TOKEN: new SecretValue(RELAY_TOKEN) },
    definitions: [localSandbox, stubModel, smsRelayA({ fetch: relayFetch })],
  });
  const sandbox = await base.sandbox('SANDBOX');
  const instances: string[] = [];
  const recording: SandboxProvider = {
    async start(spec) {
      const instance = await sandbox.start(spec);
      instances.push(instance.id);
      return instance;
    },
    cancel: id => sandbox.cancel(id),
    status: id => sandbox.status(id),
    cost: id => sandbox.cost(id),
  };
  const providers: Providers = { configured: iface => base.configured(iface), get: (slot, iface) => base.get(slot, iface), credentials: slot => base.credentials(slot), channel: slot => base.channel(slot), sandbox: async () => recording, model: slot => base.model(slot), files: slot => base.files(slot), principalOf: slot => base.principalOf(slot) };
  let agents: Agents | undefined;
  const notifier = createNotifier({
    orm,
    dispatch: noticeWorkQueue,
    providers,
    replies: { 'schedules.run': { close } },
    agents: { request: (tx, input) => agents!.notifierAgents.request(tx, input) },
    // A reply no keyword reads goes to a run of the reader.
    interpret: { agent: 'reader' },
  });
  let fetchApp: (request: Request) => Promise<Response | null> = async () => null;
  const app = Bun.serve({ port: 0, fetch: async request => (await fetchApp(request)) ?? new Response('not found', { status: 404 }) });
  agents = createAgents({
    orm,
    requests: agentRunRequestQueue,
    endpoint: `http://localhost:${app.port}/api/agents`,
    providers,
    notifier,
    tools: { ...tools, reply: notifier.replyTool },
    watchSeconds: 3600,
  });
  fetchApp = agents.routes();

  // The manager may close tasks; Ann may only approve. An agent's role
  // writes tasks; a reply by SMS may close one.
  const boss = String((await sys.account.createOne({ email: 'boss@example.test' })).id);
  const ann = String((await sys.account.createOne({ email: 'ann@example.test' })).id);
  const entered = String((await sys.account.createOne({ email: 'temp@example.test' })).id);
  const manager = await sys.role.createOne({ name: 'manager', permissions: ['tasks', 'agents'] });
  const approver = await sys.role.createOne({ name: 'approver', permissions: ['agents.approve'] });
  await sys.role.createOne({ name: 'reader-agent', permissions: ['tasks.read', 'tasks.write'], reaches: ['TaskNote:task'] });
  await sys.userRoleGrant.createOne({ userId: boss, roleId: manager.id, grantedAt: new Date() });
  await sys.userRoleGrant.createOne({ userId: entered, roleId: manager.id, grantedAt: new Date() });
  await sys.userRoleGrant.createOne({ userId: ann, roleId: approver.id, grantedAt: new Date() });
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.boss, principal: boss, trust: 'verified', verifiedAt: new Date() });
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.ann, principal: ann, trust: 'verified', verifiedAt: new Date() });
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.entered, principal: entered, trust: 'entered', verifiedAt: new Date() });
  await sys.noticePreference.createOne({ principal: null, eventKind: '*', urgency: '*', channels: ['sms'] });
  await sys.channelTrust.createOne({ channelKind: 'sms', level: 'standard', permissions: ['tasks.write'] });
  await sys.noticeRoute.createOne({ name: 'A task review reaches its people', eventKind: 'schedules.run', subjectTable: 'Task', recipients: [{ principal: boss }, { principal: ann }, { principal: entered }] });
  const total = await sys.agentBudget.createOne({ name: 'every agent', budgetLimit: 100000 });
  await sys.agentControl.createOne({ agent: null, totalId: total.id });
  await agents.define({ subject: boss, kind: 'user', permissions: ['tasks', 'agents'] }, { name: 'reader', role: 'reader-agent', tools: ['reply', 'note_task'], command: [BUN, 'agent_runner.ts'] });

  const a = String((await sys.task.createOne({ title: 'Pump overhaul', details: 'replace the pump seal', state: 'open', createdAt: new Date() })).id);
  const b = String((await sys.task.createOne({ title: 'Pump spare', details: 'a pump in storage', state: 'open', createdAt: new Date() })).id);
  return { db, orm, sys, agents, notifier, sandbox, instances, sms, stopApp: () => app.stop(true), ids: { a, b, boss, ann, entered } };
}

/** Runs every due message of the notifier's and the dispatcher's queues until none is due. */
async function drain(w: World): Promise<void> {
  for (let round = 0; round < 50; round++) {
    let handled = 0;
    for (const queue of [w.notifier.workerQueue(w.db.source), w.agents.workerQueue(w.db.source)]) {
      for (const claim of await queue.claim(20, 60)) {
        await claim.handle(new AbortController().signal);
        await queue.complete(claim.id, claim.token);
        handled++;
      }
    }
    if (handled === 0) return;
  }
  throw new Error('the queues did not drain');
}

/** Waits until cond holds. */
async function until(cond: () => Promise<boolean>, timeoutMs = 20_000): Promise<void> {
  const end = Date.now() + timeoutMs;
  while (!(await cond())) {
    if (Date.now() > end) throw new Error('timed out');
    await sleep(50);
  }
}

/** Raises a review of task key, which notifies its people by SMS: resolves to each one's notice reference. */
async function review(w: World, key: string): Promise<Record<string, number>> {
  await w.orm.transaction(tx => w.notifier.fromBehaviorEvent(tx, 'Task', 'schedules', { kind: 'run', key, payload: {} }));
  await drain(w);
  const out: Record<string, number> = {};
  for (const who of ['boss', 'ann', 'entered']) {
    const notice = (await w.sys.notice.findMany({ recipient: { eq: w.ids[who] }, subjectKey: { eq: key } })).rows[0];
    if (notice) out[who] = Number(notice.reference);
  }
  return out;
}

async function replyRow(w: World, id: string) {
  return (await w.sys.reply.findOne({ id: { eq: id } }))!;
}

/** Waits for a reply's run to end, as the dispatcher's watch finds it, and for the reply to be answered. */
async function answered(w: World, reply: string): Promise<void> {
  await until(async () => (await w.sys.agentRun.findMany({ trigger: { eq: 'reply' } })).rows.some(r => (r.input as any)?.reply === reply));
  await until(async () => {
    for (const id of w.instances) {
      const { state } = await w.sandbox.status(id);
      if (state === 'running' || state === 'starting') return false;
    }
    return true;
  });
  await w.db.raw(`UPDATE agent_run_request_queue SET due_at = now() WHERE step = 'watch' AND state = 'ready'`);
  await drain(w);
  await until(async () => (await replyRow(w, reply)).outcome !== 'interpreting');
}

const said = (w: World, to: string) => w.sms.filter(m => m.to === to).map(m => m.text);

describe.skipIf(!available)('a reply interpreter other than keywords', () => {
  let w: World | undefined;
  afterEach(async () => {
    if (!w) return;
    for (const id of w.instances) await w.sandbox.cancel(id).catch(() => {});
    w.stopApp();
    await w.db.drop();
    w = undefined;
  });

  test("a reply no keyword reads is read by an agent run that acts for its sender, and its action is repeated back with an undo", async () => {
    w = await world();
    const { a } = w.ids;
    const refs = await review(w, a);
    // The model stub calls the tool the reply names; a model would read it.
    const outcome = await w.notifier.reply({ channel: 'sms', from: PHONE.boss, body: 'call reply {"verb":"close"}' });
    expect(outcome).toMatchObject({ outcome: 'interpreting', principal: { kind: 'user', id: w.ids.boss } });
    // The reply is the audit's at once: its channel, its sender and what reads it.
    expect(await replyRow(w, outcome.reply!)).toMatchObject({ channelKind: 'sms', interpreter: 'agent:reader', outcome: 'interpreting', reference: refs.boss });
    await drain(w);
    await answered(w, outcome.reply!);
    // The run acted for the sender, about the notice's record alone.
    const run = (await w.sys.agentRun.findMany({ trigger: { eq: 'reply' } })).rows[0]!;
    expect(run).toMatchObject({ trigger: 'reply', triggerTable: 'Task', triggerKey: a, actingForKind: 'user', actingFor: w.ids.boss, status: 'done' });
    expect(run.targets).toEqual([{ table: 'Task', key: a }]);
    expect((run.input as any).reply).toBe(outcome.reply);
    // The task is closed, the notice done, and the audit names the channel, the interpreter and the verb.
    expect((await w.sys.task.findOne({ id: { eq: a } }))!.state).toBe('closed');
    expect((await w.sys.notice.findOne({ id: { eq: outcome.notice! } }))!.state).toBe('done');
    expect(await replyRow(w, outcome.reply!)).toMatchObject({ channelKind: 'sms', interpreter: 'agent:reader', verb: 'close', outcome: 'done', principal: w.ids.boss });
    // Repeated back with its undo, which a keyword reply takes.
    expect(said(w, PHONE.boss)).toContain(`Closed ${refs.boss}: ${(await w.sys.notice.findOne({ id: { eq: outcome.notice! } }))!.title}. Reply UNDO ${refs.boss} to undo.`);
    const undone = await w.notifier.reply({ channel: 'sms', from: PHONE.boss, body: `undo ${refs.boss}` });
    expect(undone).toMatchObject({ outcome: 'done', verb: 'undo' });
    expect((await w.sys.task.findOne({ id: { eq: a } }))!.state).toBe('open');
  });

  test("keywords stay the default, and an identity entered by hand is no model's to read", async () => {
    w = await world();
    const { a } = w.ids;
    const refs = await review(w, a);
    const keyword = await w.notifier.reply({ channel: 'sms', from: PHONE.boss, body: `close ${refs.boss}` });
    expect(keyword).toMatchObject({ outcome: 'done', verb: 'close' });
    expect(await replyRow(w, keyword.reply!)).toMatchObject({ interpreter: 'keyword' });
    const entered = await w.notifier.reply({ channel: 'sms', from: PHONE.entered, body: 'please shut it' });
    expect(entered.outcome).toBe('refused');
    expect(await replyRow(w, entered.reply!)).toMatchObject({ interpreter: 'keyword', outcome: 'refused' });
    expect((await w.sys.agentRun.findMany({ trigger: { eq: 'reply' } })).rows).toEqual([]);
  });

  test("the run holds its sender's permissions, and its scope does not widen", async () => {
    w = await world();
    const { a, b } = w.ids;
    const refs = await review(w, a);
    // Ann may not close a task: her run, the reader's role narrowed to her
    // permissions, is refused, and she is told.
    const ann = await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'call reply {"verb":"close"}' });
    await drain(w);
    await answered(w, ann.reply!);
    expect(await replyRow(w, ann.reply!)).toMatchObject({ outcome: 'refused', detail: `You may not close ${refs.ann}.` });
    expect((await w.sys.task.findOne({ id: { eq: a } }))!.state).toBe('open');
    expect(said(w, PHONE.ann)).toContain(`You may not close ${refs.ann}.`);
    // The manager's run reaches the notice's record and nothing else: a
    // write to another task is refused, and the reply ends not acted on.
    const wide = await w.notifier.reply({ channel: 'sms', from: PHONE.boss, body: `call note_task {"key":"${b}","details":"moved"}` });
    await drain(w);
    await answered(w, wide.reply!);
    expect((await w.sys.task.findOne({ id: { eq: b } }))!.details).toBe('a pump in storage');
    expect(await replyRow(w, wide.reply!)).toMatchObject({ outcome: 'refused', detail: 'the interpreter did not act on it' });
    expect(said(w, PHONE.boss).at(-1)).toBe(`Your reply was not acted on (${refs.boss}). Reply with a word it takes, or use the app.`);
    const runs = (await w.sys.agentRun.findMany({ trigger: { eq: 'reply' } }, { orderBy: [{ field: 'createdAt' }] })).rows;
    expect(runs.map(r => r.targets)).toEqual([[{ table: 'Task', key: a }], [{ table: 'Task', key: a }]]);
  });
});
