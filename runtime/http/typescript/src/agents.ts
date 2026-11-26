import type { Authenticator, GrantReach, Principal } from './auth.js';
import { covers } from './auth.js';
import { activeGrants, grantAuthenticator, issueGrant, reachesOf, revokeGrant } from './access.js';
import type { BucketBody } from './bucket.js';
import { withCaller } from './caller.js';
import { createLogger, type Logger } from './logger.js';
import { serveMcp, type McpCallGate, type McpCallOutcome, type McpTool, type McpToolSet } from './mcp.js';
import { canonicalJson } from './mcpconfirm.js';
import type { Notifier, ReplyAction } from './notify.js';
import type { RequestContext } from './operation.js';
import type { ModelCompletion, ModelMessage, ModelTool, ModelUsage } from './providers/model.js';
import type { Providers } from './providers/registry.js';
import type { SandboxCost, SandboxProvider } from './providers/sandbox.js';
import type { RowRef } from './rowfeatures.js';
import { enqueue, postgresQueue, type QueueDefinition, type QueuePool } from './queue.js';
import type { CoreCaller } from './rules.js';
import { callSearchTool, searchMcpTool, type SearchOptions } from './search-tables.js';
import { SecretValue } from './secret.js';
import { sha256 } from './sha256.js';
import type { WorkerMessage, WorkerQueue } from './worker.js';

/*
Agent orchestration (D158, D77's dispatcher): agents are rows, each run gets
a fresh sandbox instance and a token held to its task, and autonomy is set
per operation, with budgets and stop switches.

The tables are the database's classes marked @agents("<part>"), whose fields
the loader fills: the definitions (a @versioned table, so a run records the
version it ran), the runs, the dispatcher's request queue, the autonomy
policies, the approvals an ask writes, and the controls (one row per agent
and one for every agent: the stop switch, and a Budget whose meter each
agent's usage counts in, and every agent's through its scope).

Triggers. A run request is a message of the request queue: a route naming
an agent (the notifier's { agent } recipient, D153), a row assigned to an
agent, a schedule, a principal asking, or an inbound message from a party
(D157), { trigger: "inbound", party, message, grant }, which names no agent:
the definition whose inbound is set takes it. request() enqueues one in the
caller's transaction. A principal of kind agent may not request a run: only
the dispatcher starts one, so no run widens its own reach by starting
another.

The dispatcher is the queue's worker. It claims a request, refuses it while
the agent or every agent is paused or over its budget, writes the run row,
holds the run's Lease (D104) while its instance runs, mints the run's token
(a grant per target row, D156, held by the run, ended when the run ends),
and starts a sandbox instance (D159) with the definition, the token, the
endpoint's URL and the credential references, never their values: the
sandbox resolves them itself (useCredential). A failure to start is recorded
with Retries (D104), whose backoff sets when the start is tried again. A
watch message polls the instance until it ends; the dispatcher then revokes
the token, meters the instance's cost, writes the redacted transcript to the
bucket and records the outcome.

The run's principal. authenticator() resolves the token to the grant's
holder, the run, as kind agent, acting for whom the run acts for, holding
the agent's role's permissions narrowed to those whom it acts for holds, and
reaching the run's targets and the rows the role's paths reach from them
(every grant the run holds). The repositories keep its reads and writes to
that reach and to each row's audience and agent access (followRole,
readOnly, none), as for any agent run (D156).

Autonomy. Each tool call passes gate: the most specific policy row for the
agent and the operation or its class (read, internalWrite, messageParty,
moneyMovement) says auto, tell or ask, else the operation's own default.
An operation that moves money is ask whatever a row says. ask writes an
approval holding the exact parameters and their hash, raises
approval.requested (urgent) on it for the routes to send to those who hold
its permission, and answers 428 approval_required; the same call runs once
an approval of the same hash is approved, and spends it. A call with any
other parameter finds no approval of its hash: the earlier ones are
superseded and it asks again. Approving is approve(), which a reply action
runs on any channel (replyActions). This is D93's confirmation for calls a
key or a token makes (D114).

Budgets. Each turn's usage is recorded on the agent's control row's Budget
meter, which counts in every agent's too (its scope). A warning event fires
as usage crosses the row's warnAt percent; at the agent's cap the agent
pauses; at every agent's cap, a scheduled run (route, assigned, schedule,
inbound) is refused and a principal's run waits for an approval before it
starts.

Stop switches. pause(agent) or pause(null) sets the control row; every run
in flight of the agent (or of every agent) is cancelled through its sandbox
provider, its token revoked, and the row it was assigned returned to its
queue (onReturn).

Asking a principal. The ask_principal tool posts the question in the row's
thread (its Comments) and enqueues a follow-up on the request queue, due at
the deadline; at the deadline a thread with no answer gets a follow-up row
(followUp).

Outside content is data. A tool marked untrusted (an inbound message, a web
page, a document) has its results marked untrusted in the run and in every
model turn after it, and counted on the run. No tool widens a run's scope:
every tool runs as the run's principal, whose grants were fixed at start.

Administering agents is permissions: agents.define, agents.autonomy,
agents.budgets, agents.credentials and agents.pause, and agents.approve, an
approval's default.

Transcripts are redacted (the run's token, bearer tokens, and the patterns
the options add) as each entry is written, kept on the run row while it
runs and in the bucket after, for transcriptRetentionDays (30), after which
prune deletes the object and stamps the row; the run row itself is history
and stays.
*/

/** The triggers of a run (D158). */
export type AgentTrigger = 'route' | 'assigned' | 'schedule' | 'principal' | 'inbound';


/** Who asked for a run, or whom it acts for. */
export interface PrincipalRef {
  readonly kind: 'user' | 'party' | 'agent' | 'route';
  readonly subject: string;
  /** A party's table. */
  readonly table?: string;
}

/**
 * A message of the dispatcher's queue: a run request, whose step is start
 * (or null), and the dispatcher's own watch and followUp steps. Section 7's
 * inbound message (D157) is { trigger: 'inbound', party, message, grant }.
 */
export interface AgentRunRequest {
  readonly step?: 'start' | 'watch' | 'followUp' | null;
  /** The agent, by name or key; absent for an inbound message, which the definition whose inbound is set takes. */
  readonly agent?: string | null;
  readonly trigger?: AgentTrigger | null;
  /** The row the run is about, which its timeline shows. */
  readonly subjectTable?: string | null;
  readonly subjectKey?: string | null;
  /** More rows of the run's scope. */
  readonly targets?: readonly RowRef[] | null;
  /** A route's event. */
  readonly event?: unknown;
  /** What the run is told: { task }, the first message of its turns. */
  readonly input?: Readonly<Record<string, unknown>> | null;
  readonly requestedBy?: PrincipalRef | null;
  readonly actingFor?: PrincipalRef | null;
  /** An inbound message's party, { table, key } (D157). */
  readonly party?: RowRef | null;
  /** An inbound message: its row as { table, key }, or its text as { body }. */
  readonly message?: unknown;
  /** The party's active grant, whose target is the run's scope. */
  readonly grant?: string | null;
  /** The run a watch, a retry, a follow-up or an approved start is of. */
  readonly run?: string | null;
  /** An ask's deadline. */
  readonly ask?: { readonly table: string; readonly key: string; readonly message: string; readonly question: string } | null;
}

/** The classes of operation an autonomy policy names (D158). */
export type OperationClass = 'read' | 'internalWrite' | 'messageParty' | 'moneyMovement';

/** The autonomy an agent has over an operation. */
export type Autonomy = 'auto' | 'tell' | 'ask';

/** What a tool of the app is given: a transaction as the run's principal, the principal, the run and the arguments. */
export interface AgentToolContext {
  /** A transaction acting as the run's principal, whose reads and writes keep to its reach. */
  readonly tx: any;
  readonly principal: Principal;
  readonly run: Readonly<Record<string, unknown>>;
  readonly args: Readonly<Record<string, unknown>>;
  /** The behaviors' caller of the run: its subject and permissions, for an operation of a behavior. */
  readonly caller: CoreCaller;
}

/** A tool of the app an agent may call through the run's MCP endpoint. */
export interface AgentTool {
  readonly description: string;
  readonly inputSchema: Readonly<Record<string, unknown>>;
  /** What the tool is to the autonomy policy. */
  readonly class: OperationClass;
  /** The autonomy when no policy row names it; the class's default when absent. */
  readonly mode?: Autonomy;
  /** A permission the run's principal holds for the tool to be offered. */
  readonly permission?: string;
  /** Its results carry outside content (an inbound message, a web page, a document), which the run marks untrusted. */
  readonly untrusted?: boolean;
  run(ctx: AgentToolContext): Promise<unknown>;
}

/** A tool the sandbox runs itself (a browser, a fetch with a credential): offered to the model, run inside the instance. */
export interface AgentSandboxTool {
  readonly name: string;
  readonly description: string;
  readonly inputSchema: Readonly<Record<string, unknown>>;
  readonly untrusted?: boolean;
}

/** Where a run's transcript is kept once it ends: a bucket, or its put, get and delete. */
export interface TranscriptStore {
  put(name: string, body: BucketBody, options?: { readonly contentType?: string }): Promise<unknown>;
  get(name: string): Promise<{ readonly body: ReadableStream<Uint8Array> } | { text(): Promise<string> } | unknown>;
  delete(name: string): Promise<void>;
}

/** One entry of a run's transcript, redacted as it is written. */
export interface TranscriptEntry {
  readonly at: string;
  readonly kind: 'task' | 'message' | 'completion' | 'call' | 'credential' | 'note';
  readonly role?: ModelMessage['role'];
  readonly content?: string;
  readonly toolCallId?: string;
  readonly toolCalls?: readonly { readonly id: string; readonly name: string; readonly input: unknown }[];
  readonly tool?: string;
  readonly args?: unknown;
  readonly outcome?: string;
  readonly untrusted?: boolean;
  readonly ref?: string;
}

/** How the app asks a principal in a row's thread; the default posts on the row's Comments. */
export interface AskThread {
  /** Posts question on row as the run, mentioning principal; resolves to the message's id. */
  post(tx: any, input: { readonly row: RowRef; readonly question: string; readonly principal?: string; readonly caller: CoreCaller }): Promise<string>;
  /** Whether someone other than the run answered the message. */
  answered(tx: any, input: { readonly row: RowRef; readonly message: string; readonly run: string }): Promise<boolean>;
}

/** What createAgents takes. */
export interface AgentsOptions {
  /** The generated createOrm's ORM of the database holding the @agents tables, the @accessGrants table and the UserRole table. */
  readonly orm: any;
  /** The @agents("requests") queue's definition. */
  readonly requests: QueueDefinition<any>;
  /** The URL the sandbox reaches the agent routes at: its turns, its MCP endpoint, its reports. */
  readonly endpoint: string;
  /** The environment's providers (D159): the sandbox and model slots the definitions name, or the defaults below. */
  readonly providers?: Providers;
  /** The sandbox slot of a definition that names none; the only sandbox slot configured unless set. */
  readonly sandboxSlot?: string;
  /** The model slot of a definition that names none; the only model slot configured unless set. */
  readonly modelSlot?: string;
  /** The notifier an approval's request, a budget warning and a tell are raised through. */
  readonly notifier?: Notifier;
  /** Where a run's transcript goes when it ends; on the run row alone when absent. */
  readonly transcripts?: TranscriptStore;
  /** The app's tools, by name. */
  readonly tools?: Readonly<Record<string, AgentTool>>;
  /** The tools the sandbox runs itself. */
  readonly sandboxTools?: readonly AgentSandboxTool[];
  /** Search across tables (D155) as the tool search, run as the run's principal: its options, or true for none. */
  readonly search?: SearchOptions | true;
  /** How a principal is asked in a row's thread; the row's Comments when absent. */
  readonly thread?: AskThread;
  /** Makes the follow-up row of an ask nobody answered by its deadline. */
  readonly followUp?: (tx: any, input: { readonly run: Readonly<Record<string, unknown>>; readonly row: RowRef; readonly question: string }) => Promise<RowRef | void>;
  /** Returns a row assigned to an agent to its queue when the agent is paused and its run cancelled. */
  readonly onReturn?: (tx: any, input: { readonly run: Readonly<Record<string, unknown>>; readonly row: RowRef }) => Promise<void>;
  /** Plain settings and secrets of the sandbox beside the run's own: a credential provider's slot settings, say. */
  readonly sandboxSpec?: (run: Readonly<Record<string, unknown>>, definition: Readonly<Record<string, unknown>>) => { readonly env?: Readonly<Record<string, string>>; readonly secrets?: Readonly<Record<string, SecretValue<string>>> };
  /** The behaviors' caller the dispatcher writes runs and controls as, and holds a run's lease as. */
  readonly dispatcher?: CoreCaller;
  /** The units a turn's usage costs; its tokens unless set. */
  readonly costOf?: (usage: ModelUsage) => number;
  /** The units an instance costs; its wall seconds unless set. */
  readonly sandboxCostOf?: (cost: SandboxCost) => number;
  /** The Budget meter of the controls; cost unless set. */
  readonly meter?: string;
  /** How often a running instance is watched, in seconds; 5 unless set. */
  readonly watchSeconds?: number;
  /** How long a run waits for an answer to its question, in seconds; 86400 unless set. */
  readonly askSeconds?: number;
  /** How long a pending or approved approval holds, in seconds; 86400 unless set. */
  readonly approvalTtlSeconds?: number;
  /** How long a transcript is kept after its run ends, in days; 30 unless set. */
  readonly transcriptRetentionDays?: number;
  /** More patterns a transcript entry's text is redacted of. */
  readonly redact?: readonly RegExp[];
  readonly now?: () => Date;
  readonly log?: Logger;
}

/** What gate answers: run the call, or the refusal the call answers instead. */
export type GateOutcome =
  | { readonly run: true; readonly mode: Autonomy; readonly approval?: string }
  | { readonly run: false; readonly status: number; readonly code: string; readonly detail: string; readonly approval?: string };

/** The orchestration. */
export interface Agents {
  /** Puts a run request on the dispatcher's queue in the caller's transaction; a principal of kind agent may not. */
  request(tx: any, input: AgentRunRequest, options?: { readonly delaySeconds?: number }): Promise<void>;
  /** Handles one message of the request queue: the dispatcher's worker handler. */
  handle(message: AgentRunRequest): Promise<void>;
  /** The request queue as a worker runs it. */
  workerQueue(pool: QueuePool): WorkerQueue;
  /** The authenticator of a run's token: kind agent, acting for whom the run acts for, its permissions narrowed and its grants the run's; inner establishes every other caller. */
  authenticator(inner?: Authenticator): Authenticator;
  /** The principal a request's run token resolves to, or null. */
  principalOf(request: Request): Promise<Principal | null>;
  /** The agent routes: POST <endpoint>/turn, /mcp, /credential, /finish, and with authenticate, /approvals/<id>/approve and /deny. */
  routes(options?: { readonly authenticate?: (request: Request) => Promise<Principal | null> }): (request: Request) => Promise<Response | null>;
  /** The MCP tool set a run's principal is served. */
  mcpTools(principal: Principal): Promise<McpToolSet>;
  /**
   * The call gate of a generated API's MCP endpoint (mountMcp's callGate):
   * a call an agent run makes, with its token, is held to the run's
   * autonomy policy by its tool's @autonomy; every other caller's runs.
   */
  readonly mcpGate: McpCallGate;
  /** Holds a call of operation with args to the run's autonomy policy. */
  gate(principal: Principal, operation: string, args: Readonly<Record<string, unknown>>, options?: { readonly class?: OperationClass; readonly mode?: Autonomy }): Promise<GateOutcome>;
  approve(principal: Principal, approval: string, options?: { readonly tx?: any; readonly via?: string }): Promise<void>;
  deny(principal: Principal, approval: string, options?: { readonly tx?: any; readonly via?: string }): Promise<void>;
  /** The reply actions approve and deny of approval.requested, for createNotifier({ replies }). */
  readonly replyActions: { readonly approve: ReplyAction; readonly deny: ReplyAction };
  /** Pauses an agent, or every agent for null, cancelling its runs in flight. */
  pause(principal: Principal, agent: string | null, reason?: string): Promise<void>;
  resume(principal: Principal, agent: string | null): Promise<void>;
  /** Writes an autonomy policy row; refuses one that lets an agent move money without asking. */
  setPolicy(principal: Principal, policy: { readonly agent?: string | null; readonly operation?: string; readonly operationClass?: OperationClass; readonly mode: Autonomy; readonly permission?: string }): Promise<Readonly<Record<string, unknown>>>;
  /** Sets an agent's budget, or every agent's for null. */
  setBudget(principal: Principal, agent: string | null, budget: { readonly limit: number | null; readonly warnAt?: number }): Promise<void>;
  /** Writes or edits an agent's definition; its credential references need agents.credentials too. */
  define(principal: Principal, definition: Readonly<Record<string, unknown>>): Promise<Readonly<Record<string, unknown>>>;
  /** Ends a run in flight: cancels its instance and revokes its token. */
  cancel(run: string, outcome?: string, detail?: string): Promise<void>;
  /** A run's redacted transcript. */
  transcript(run: string): Promise<TranscriptEntry[]>;
  /** Deletes up to limit transcripts past their retention; resolves to how many. */
  prune(limit: number): Promise<number>;
  /** What createNotifier's agents option takes: a route's agent recipient enqueues a request. */
  readonly notifierAgents: { request(tx: any, input: AgentRunRequest): Promise<void> };
}

/** Thrown for a refused administrative call or request. */
export class AgentsError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'AgentsError';
    this.status = status;
    this.code = code;
  }
}

/** The permissions of administering agents (D158). */
export const AGENT_PERMISSIONS = {
  define: 'agents.define',
  autonomy: 'agents.autonomy',
  budgets: 'agents.budgets',
  credentials: 'agents.credentials',
  pause: 'agents.pause',
  approve: 'agents.approve',
} as const;

/** How long a transcript is kept after its run ends, in days. */
export const DEFAULT_TRANSCRIPT_RETENTION_DAYS = 30;

/** The default autonomy of a class: a read and an internal write run; a message to a party and a money movement ask. */
export function defaultAutonomy(cls: OperationClass): Autonomy {
  return cls === 'read' || cls === 'internalWrite' ? 'auto' : 'ask';
}

/** The SHA-256, in hex, of an operation and its parameters as canonical JSON: what an approval is bound to. */
export function paramsHash(operation: string, params: unknown): string {
  return hex(sha256(new TextEncoder().encode(canonicalJson({ operation, params }))));
}

function hex(bytes: Uint8Array): string {
  return [...bytes].map(b => b.toString(16).padStart(2, '0')).join('');
}

type Values = Record<string, any>;

interface RepositoryShape {
  readonly def: { readonly name: string; readonly key: string; readonly agents?: string; readonly access?: string; readonly relations?: readonly { readonly field: string; readonly column: string; readonly target: string }[] };
  findOne(filter: Values, options?: Values): Promise<Values | null>;
  findMany(filter: Values, options?: Values): Promise<{ rows: Values[] }>;
  createOne(input: Values): Promise<Values>;
  updateOne(id: unknown, update: Values, options?: Values): Promise<Values>;
  [use: string]: any;
}

function repositoriesOf(orm: any): [string, RepositoryShape][] {
  return Object.entries(orm).filter((entry): entry is [string, RepositoryShape] => {
    const value = entry[1] as RepositoryShape | undefined;
    return typeof value === 'object' && value !== null && typeof value.def === 'object' && typeof value.findOne === 'function';
  });
}

const DEFAULT_DISPATCHER: CoreCaller = { subject: 'agent-dispatcher', permissions: [] };

const TOKEN_PATTERNS: readonly RegExp[] = [/grant_[A-Za-z0-9_-]{16,}/gu, /Bearer\s+[A-Za-z0-9._~+/=-]+/gu];

/** Builds the orchestration over options.orm. */
export function createAgents(options: AgentsOptions): Agents {
  const orm = options.orm;
  const log = options.log ?? createLogger({ component: 'agents' });
  const now = options.now ?? (() => new Date());
  const meter = options.meter ?? 'cost';
  const dispatcher = options.dispatcher ?? DEFAULT_DISPATCHER;
  const watchSeconds = options.watchSeconds ?? 5;
  const askSeconds = options.askSeconds ?? 86_400;
  const approvalTtl = options.approvalTtlSeconds ?? 86_400;
  const retentionDays = options.transcriptRetentionDays ?? DEFAULT_TRANSCRIPT_RETENTION_DAYS;
  const costOf = options.costOf ?? ((usage: ModelUsage) => usage.inputTokens + usage.outputTokens);
  const sandboxCostOf = options.sandboxCostOf ?? ((cost: SandboxCost) => Math.ceil(cost.wallSeconds));
  const endpoint = options.endpoint.replace(/\/+$/u, '');
  const base = new URL(endpoint).pathname.replace(/\/+$/u, '');
  const tools = options.tools ?? {};
  const sandboxTools = options.sandboxTools ?? [];

  const all = repositoriesOf(orm);
  const memberOf = (part: string): string | undefined => all.find(([, r]) => r.def.agents === part)?.[0];
  const parts = {
    definitions: memberOf('definitions'),
    runs: memberOf('runs'),
    policies: memberOf('policies'),
    approvals: memberOf('approvals'),
    controls: memberOf('controls'),
  };
  for (const [part, member] of Object.entries(parts)) {
    if (!member) throw new TypeError(`createAgents: the ORM has no @agents("${part}") table`);
  }
  const tableMember = (type: string): string | undefined => all.find(([, r]) => r.def.name === type)?.[0];
  const rolesMember = all.find(([, r]) => r.def.access === 'roles')?.[0];
  const sys = (db: any): any => db.asSystem();
  /** The ORM as the dispatcher: the system actor, whose behaviors' commands run as the dispatcher's caller. */
  const self = (db: any): any => sys(db).withBehaviorCaller(dispatcher);
  const repo = (db: any, member: string | undefined): RepositoryShape => db[member!] as RepositoryShape;
  const defs = () => repo(self(orm), parts.definitions);
  const runs = (db: any = orm) => repo(self(db), parts.runs);
  const runsDef = all.find(([m]) => m === parts.runs)![1].def;
  const controlsDef = all.find(([m]) => m === parts.controls)![1].def;
  /** The relation of the controls to the table of every agent's budget, its Budget meter's scope. */
  const totalRelation = controlsDef.relations?.find(r => r.target !== parts.controls);
  const definitionOf = (type: string) => all.find(([, r]) => r.def.name === type)?.[1].def;

  // ---- redaction and the transcript ----

  const redactText = (text: string, secrets: readonly string[] = []): string => {
    let out = text;
    for (const secret of secrets) if (secret) out = out.split(secret).join('[redacted]');
    for (const pattern of [...TOKEN_PATTERNS, ...(options.redact ?? [])]) out = out.replace(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : pattern.flags + 'g'), '[redacted]');
    return out;
  };
  const redact = <T>(value: T, secrets: readonly string[] = []): T => {
    if (typeof value === 'string') return redactText(value, secrets) as T;
    if (value === null || value === undefined || typeof value !== 'object') return value;
    return JSON.parse(redactText(JSON.stringify(value), secrets)) as T;
  };
  const append = async (db: any, runId: string, entries: readonly Omit<TranscriptEntry, 'at'>[]): Promise<void> => {
    await db.transaction(async (tx: any) => {
      const row = await runs(tx).findOne({ id: { eq: runId } });
      if (!row) return;
      const at = now().toISOString();
      const next = [...((row.transcript as TranscriptEntry[] | null) ?? []), ...entries.map(entry => redact({ at, ...entry }))];
      await runs(tx).updateOne(runId, { transcript: next });
    });
  };

  // ---- definitions, controls and budgets ----

  const findDefinition = async (agent: string): Promise<Values | null> => {
    const byName = await defs().findOne({ name: { eq: agent } });
    if (byName) return byName;
    try {
      return await defs().findOne({ id: { eq: agent } });
    } catch {
      return null;
    }
  };

  const controlOf = async (db: any, agent: string | null): Promise<Values> => {
    const controls = repo(self(db), parts.controls);
    const found = await controls.findOne(agent === null ? { agent: { isNull: true } } : { agent: { eq: agent } });
    if (found) return found;
    const every = agent === null ? null : await controlOf(db, null);
    return controls.createOne({
      agent,
      ...(totalRelation && every && every[totalRelation.column] !== undefined ? { [totalRelation.column]: every[totalRelation.column] } : {}),
    });
  };

  /** Whether a Budget repository's meter fits one more unit; true for one with no Budget. */
  const fits = async (repository: RepositoryShape | undefined, key: unknown): Promise<boolean> => {
    if (!repository?.budget?.checkReserve || key === null || key === undefined) return true;
    const answer = await repository.budget.checkReserve(key, { meter, amount: 1 }, { caller: dispatcher });
    return Boolean(answer?.fits);
  };

  const totalOf = (db: any, control: Values): { repository?: RepositoryShape; key?: unknown } => {
    if (!totalRelation) return {};
    return { repository: repo(self(db), totalRelation.target), key: control[totalRelation.column] };
  };

  /** Where the agent stands: paused (itself or every agent), and whether it or every agent is over its cap. */
  const standing = async (db: any, agent: string): Promise<{ paused: boolean; reason?: string; agentCapped: boolean; totalCapped: boolean }> => {
    const every = await controlOf(db, null);
    const own = await controlOf(db, agent);
    const controls = repo(self(db), parts.controls);
    const total = totalOf(db, totalRelation && own[totalRelation.column] ? own : every);
    const totalCapped = !(await fits(total.repository, total.key));
    // The agent's own meter answers for every scope it spends within: past every agent's cap it fits nowhere, which is not its own cap.
    const agentCapped = !totalCapped && !(await fits(controls, own.id));
    if (every.paused) return { paused: true, reason: String(every.pausedReason ?? 'stopped'), agentCapped, totalCapped };
    if (own.paused) return { paused: true, reason: String(own.pausedReason ?? 'stopped'), agentCapped, totalCapped };
    return { paused: false, agentCapped, totalCapped };
  };

  const raise = async (tx: any, table: string, key: string, kind: string, payload?: Values, urgency?: 'urgent' | 'normal'): Promise<void> => {
    if (!options.notifier) return;
    const declared = (definitionOf(table) as { events?: readonly { kind: string }[] } | undefined)?.events?.some(e => e.kind === kind);
    if (!declared) return;
    await options.notifier.raise(tx, { table, key, kind, ...(payload ? { payload } : {}), ...(urgency ? { urgency } : {}) });
  };

  /** Records usage on the agent's control row: warns at its threshold and pauses the agent at its cap. */
  const meterUsage = async (agent: string, amount: number): Promise<void> => {
    if (amount <= 0) return;
    const controls = repo(self(orm), parts.controls);
    if (!controls.budget?.recordUsage) return;
    const own = await controlOf(orm, agent);
    const used = await controls.budget.recordUsage(own.id, { meter, amount }, { caller: dispatcher });
    const limit = own.budgetLimit === null || own.budgetLimit === undefined ? undefined : Number(own.budgetLimit);
    const after = Number(used?.used ?? 0);
    const before = after - amount;
    const warnAt = Number(own.warnAt ?? 80);
    if (limit !== undefined && limit > 0 && before * 100 < warnAt * limit && after * 100 >= warnAt * limit) {
      await orm.transaction((tx: any) => raise(tx, controlsDef.name, String(own.id), 'agent.budgetWarning', { used: after, limit, warnAt }));
    }
    const overruns = (used?.overruns as { schema: string; id: string }[] | undefined) ?? [];
    if (overruns.some(o => o.schema === controlsDef.name && String(o.id) === String(own.id)) || (limit !== undefined && after >= limit)) {
      await setPaused(agent, true, 'budget', 'budget');
    }
  };

  // ---- runs ----

  const sandboxOf = async (definition: Values): Promise<{ slot: string; provider: SandboxProvider }> => {
    if (!options.providers) throw new Error('createAgents: no providers, and a run needs a sandbox provider (D159)');
    const slot = (definition.sandboxSlot as string | null) ?? options.sandboxSlot ?? options.providers.configured('sandbox')[0]?.slot;
    if (!slot) throw new Error('createAgents: no sandbox slot is configured');
    return { slot, provider: await options.providers.sandbox(slot) };
  };
  const modelOf = async (definition: Values) => {
    if (!options.providers) throw new Error('createAgents: no providers, and a run needs a model provider (D159)');
    const slot = (definition.modelSlot as string | null) ?? options.modelSlot ?? options.providers.configured('model')[0]?.slot;
    if (!slot) throw new Error('createAgents: no model slot is configured');
    return { slot, provider: await options.providers.model(slot) };
  };

  /** Calls an operation of the run's Lease as the dispatcher, presenting the row's current token; nothing for a runs table with no Lease. */
  const leaseCall = async (runId: string, operation: 'acquire' | 'heartbeat' | 'release'): Promise<void> => {
    const runsRepo = runs();
    if (!runsRepo.lease?.[operation]) return;
    try {
      if (operation === 'acquire') {
        await runsRepo.lease.acquire(runId, {}, { caller: dispatcher });
        return;
      }
      const row = await runsRepo.findOne({ id: { eq: runId } });
      if (row?.leaseToken === null || row?.leaseToken === undefined) return;
      await runsRepo.lease[operation](runId, {}, { caller: dispatcher, preconditions: { Lease: { token: Number(row.leaseToken) } } });
    } catch (err) {
      // A lease that lapsed is Lease's to expire; one never taken has nothing to give back.
      log.warn(`a run's lease ${operation} did not hold`, { run: runId, detail: err instanceof Error ? err.message : String(err) });
    }
  };

  const targetsOf = (run: Values): RowRef[] => ((run.targets as RowRef[] | null) ?? []).map(t => ({ table: String(t.table), key: String(t.key) }));

  /**
   * Moves a run's status, through its Workflow's transition when the runs
   * table composes one (Lease's and Retries' states are its), and writes
   * extra beside it; a move its transitions lack is logged and left.
   */
  const moveRun = async (db: any, run: string, status: string, extra: Values = {}): Promise<void> => {
    const repository = runs(db);
    if (!repository.status?.transition) {
      await repository.updateOne(run, { status, ...extra });
      return;
    }
    const current = await repository.findOne({ id: { eq: run } });
    if (current && current.status !== status) {
      try {
        await repository.status.transition(run, { to: status }, { caller: dispatcher });
      } catch (err) {
        log.warn('a run did not move', { run, from: String(current.status), to: status, detail: err instanceof Error ? err.message : String(err) });
      }
    }
    if (Object.keys(extra).length > 0) await repository.updateOne(run, extra);
  };

  const finish = async (runId: string, outcome: string, detail?: string): Promise<void> => {
    const run = await runs().findOne({ id: { eq: runId } });
    if (!run || ['done', 'failed', 'cancelled'].includes(String(run.status))) return;
    for (const grant of await activeGrants(orm, { holder: runId, now: now() })) await revokeGrant(orm, String(grant.id), now());
    let cost = Number(run.cost ?? 0);
    let spent = 0;
    if (run.instance && run.sandboxSlot && options.providers) {
      try {
        const provider = await options.providers.sandbox(String(run.sandboxSlot));
        spent = sandboxCostOf(await provider.cost(String(run.instance)));
        cost += spent;
      } catch (err) {
        log.warn('a sandbox cost was not read', { run: runId, detail: err instanceof Error ? err.message : String(err) });
      }
    }
    const entries = (run.transcript as TranscriptEntry[] | null) ?? [];
    let transcriptObject: string | null = (run.transcriptObject as string | null) ?? null;
    let transcript: TranscriptEntry[] | null = entries;
    if (options.transcripts) {
      transcriptObject = `agent-runs/${runId}.json`;
      await options.transcripts.put(transcriptObject, JSON.stringify(redact(entries)), { contentType: 'application/json' });
      transcript = null;
    }
    const status = outcome === 'done' ? 'done' : outcome === 'failed' ? 'failed' : 'cancelled';
    // A run that asked and then ended runs again on its way to done or failed.
    if (run.status === 'waiting' && status !== 'cancelled') await moveRun(orm, runId, 'running');
    await moveRun(orm, runId, status, { outcome, detail: detail ?? null, cost, transcript, transcriptObject, endedAt: now() });
    // The lease is given back once the run has ended, so its end moves no state of a run in flight.
    await leaseCall(runId, 'release');
    // The instance's cost counts once the run has ended, so a cap it reaches pauses the agent without ending this run again.
    await meterUsage(String(run.agent), spent);
    if (outcome !== 'done' && run.trigger === 'assigned' && run.triggerTable && run.triggerKey && options.onReturn) {
      await orm.transaction((tx: any) => options.onReturn!(tx, { run, row: { table: String(run.triggerTable), key: String(run.triggerKey) } }));
    }
  };

  const cancel = async (runId: string, outcome = 'cancelled', detail?: string): Promise<void> => {
    const run = await runs().findOne({ id: { eq: runId } });
    if (!run || ['done', 'failed', 'cancelled'].includes(String(run.status))) return;
    if (run.instance && run.sandboxSlot && options.providers) {
      try {
        await (await options.providers.sandbox(String(run.sandboxSlot))).cancel(String(run.instance));
      } catch (err) {
        log.warn('an instance was not cancelled', { run: runId, detail: err instanceof Error ? err.message : String(err) });
      }
    }
    await finish(runId, outcome, detail);
  };

  const refuseRun = async (request: AgentRunRequest, definition: Values | null, outcome: string, detail: string): Promise<void> => {
    if (!definition) {
      log.warn('a run request names no agent', { agent: request.agent ?? null, trigger: request.trigger ?? null });
      return;
    }
    const refused = await runs().createOne(runFields(request, definition, []));
    await moveRun(orm, String(refused.id), 'cancelled', { outcome, detail, endedAt: now() });
    if (request.trigger === 'assigned' && request.subjectTable && request.subjectKey && options.onReturn) {
      const row = { table: request.subjectTable, key: request.subjectKey };
      await orm.transaction((tx: any) => options.onReturn!(tx, { run: { agent: definition.id, trigger: 'assigned' }, row }));
    }
  };

  const runFields = (request: AgentRunRequest, definition: Values, targets: RowRef[]): Values => ({
    agent: definition.id,
    agentName: definition.name,
    definitionVersion: Number(definition._version ?? 0),
    trigger: request.trigger ?? 'principal',
    triggerTable: request.subjectTable ?? request.party?.table ?? null,
    triggerKey: request.subjectKey ?? request.party?.key ?? null,
    event: request.event ?? null,
    input: { ...(request.input ?? {}), ...(request.message !== undefined && request.message !== null ? { message: request.message } : {}) },
    requestedBy: request.requestedBy?.subject ?? null,
    requestedByKind: request.requestedBy?.kind ?? null,
    actingForKind: request.actingFor?.kind ?? (request.party ? 'party' : null),
    actingFor: request.actingFor?.subject ?? request.party?.key ?? null,
    actingForTable: request.actingFor?.table ?? request.party?.table ?? null,
    targets,
  });

  /** The run's scope: the row it is about, the rows the request adds, and an inbound message's grant's target. */
  const scopeOf = async (request: AgentRunRequest): Promise<RowRef[] | string> => {
    const out: RowRef[] = [];
    const add = (ref: RowRef) => {
      if (!out.some(r => r.table === ref.table && r.key === ref.key)) out.push(ref);
    };
    if (request.trigger === 'inbound') {
      if (!request.grant || !request.party) return 'an inbound message names its party and the party\'s grant';
      const grant = (await activeGrants(orm, { holder: request.party.key, now: now() })).find(g => String(g.id) === String(request.grant));
      if (!grant) return 'the party holds no such active grant';
      add({ table: String(grant.targetType), key: String(grant.targetId) });
      return out;
    }
    if (request.subjectTable && request.subjectKey) add({ table: request.subjectTable, key: request.subjectKey });
    for (const t of request.targets ?? []) add(t);
    return out;
  };

  const start = async (request: AgentRunRequest): Promise<void> => {
    let definition: Values | null = null;
    let existing: Values | null = null;
    if (request.run) {
      existing = await runs().findOne({ id: { eq: request.run } });
      if (!existing || !['queued', 'waiting'].includes(String(existing.status))) return;
      definition = await defs().findOne({ id: { eq: existing.agent } });
    } else if (request.agent) {
      definition = await findDefinition(request.agent);
    } else if (request.trigger === 'inbound') {
      definition = (await defs().findMany({ inbound: { eq: true } }, { limit: 1, count: false, orderBy: [{ field: 'name' }] })).rows[0] ?? null;
    }
    if (!definition) return refuseRun(request, null, 'refused', 'no such agent');
    const agentId = String(definition.id);
    const stand = await standing(orm, agentId);
    const fail = async (outcome: string, detail: string) => {
      if (existing) await finish(String(existing.id), outcome, detail);
      else await refuseRun(request, definition, outcome, detail);
    };
    if (stand.paused) return fail('paused', `the agent is paused (${stand.reason})`);
    if (stand.agentCapped) {
      await setPaused(agentId, true, 'budget', 'budget');
      return fail('budget', "the agent reached its budget's cap");
    }
    const trigger = (existing?.trigger as AgentTrigger | undefined) ?? request.trigger ?? 'principal';
    if (stand.totalCapped && trigger !== 'principal') return fail('budget', "every agent's budget reached its cap, and scheduled runs stop");

    // The run row.
    let run = existing;
    if (!run) {
      const targets = await scopeOf(request);
      if (typeof targets === 'string') return refuseRun(request, definition, 'refused', targets);
      run = await runs().createOne(runFields(request, definition, targets));
      // The run on the timeline of the row that started it (D154): a reference from the run, which the row lists among its backlinks.
      if (run.triggerTable && run.triggerKey && orm.references?.add) {
        try {
          await self(orm).references.add({ from: { table: runsDef.name, key: String(run.id) }, to: { table: String(run.triggerTable), key: String(run.triggerKey) }, role: 'agentRun' });
        } catch {
          // A runs table without @references keeps no timeline entry.
        }
      }
    }
    const runId = String(run.id);

    // Every agent over its cap: a principal's run asks before it starts.
    if (stand.totalCapped && trigger === 'principal') {
      const pending = await repo(self(orm), parts.approvals).findOne({ run: { eq: runId }, operation: { eq: 'agents.overBudget' }, state: { in: ['pending', 'approved'] } });
      if (pending?.state === 'approved') {
        await repo(self(orm), parts.approvals).updateOne(pending.id, { state: 'used', usedAt: now() });
      } else {
        if (!pending) await askApproval(run, 'agents.overBudget', 'moneyMovement', { agent: definition.name, run: runId }, AGENT_PERMISSIONS.budgets, `Run ${definition.name} past every agent's budget`);
        if (run.status === 'queued') await moveRun(orm, runId, 'running');
        await moveRun(orm, runId, 'waiting');
        return;
      }
    }

    // The lease, the token and the instance.
    if (run.status === 'waiting' || run.status === 'queued') await moveRun(orm, runId, 'running');
    await leaseCall(runId, 'acquire');
    const limits = (definition.limits as Values | null) ?? {};
    const maxSeconds = Number(limits.maxSeconds ?? 3600);
    const endsAt = new Date(now().getTime() + maxSeconds * 1000);
    const targets = targetsOf(run);
    if (targets.length === 0) return finish(runId, 'refused', 'the run has no target rows, so its token would reach nothing');
    let token = '';
    const grants: string[] = [];
    for (const target of targets) {
      const issued = await issueGrant(sys(orm), { holder: runId, holderKind: 'agent', role: String(definition.role), targetType: target.table, targetId: target.key, endsAt });
      grants.push(String(issued.grant.id));
      if (!token) token = issued.token;
    }
    // The task is the transcript's first entry before the instance can ask for its first turn.
    if (!run.transcript || (run.transcript as unknown[]).length === 0) {
      await append(orm, runId, [{ kind: 'task', role: 'user', content: taskOf(run, definition), ...(run.trigger === 'inbound' ? { untrusted: true } : {}) }]);
    }
    let sandbox: { slot: string; provider: SandboxProvider };
    try {
      sandbox = await sandboxOf(definition);
      const extra = options.sandboxSpec?.(run, definition) ?? {};
      const instance = await sandbox.provider.start({
        runId,
        ...(definition.image ? { image: String(definition.image) } : {}),
        ...(Array.isArray(definition.command) ? { command: (definition.command as unknown[]).map(String) } : {}),
        env: {
          ...(extra.env ?? {}),
          AGENT_RUN_ID: runId,
          AGENT_ENDPOINT: endpoint,
          AGENT_NAME: String(definition.name),
          AGENT_DEFINITION_VERSION: String(definition._version ?? 0),
          AGENT_CREDENTIALS: JSON.stringify(((definition.credentials as unknown[] | null) ?? []).map(c => (typeof c === 'string' ? { ref: c } : c))),
        },
        secrets: { ...(extra.secrets ?? {}), AGENT_RUN_TOKEN: new SecretValue(token) },
        limits: {
          ...(limits.wallSeconds !== undefined ? { wallSeconds: Number(limits.wallSeconds) } : { wallSeconds: maxSeconds }),
          ...(limits.cpuSeconds !== undefined ? { cpuSeconds: Number(limits.cpuSeconds) } : {}),
          ...(limits.memoryMb !== undefined ? { memoryMb: Number(limits.memoryMb) } : {}),
        },
      });
      await runs().updateOne(runId, { grants, instance: instance.id, sandboxSlot: sandbox.slot, startedAt: instance.startedAt, attempts: Number(run.attempts ?? 0) + 1 });
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      log.warn('a sandbox instance did not start', { run: runId, detail });
      for (const id of grants) await revokeGrant(orm, id, now());
      await runs().updateOne(runId, { grants, attempts: Number(run.attempts ?? 0) + 1 });
      await retry(runId, request, detail);
      return;
    }
    await orm.transaction((tx: any) => enqueue(tx, options.requests, { step: 'watch', run: runId }, { delaySeconds: watchSeconds }));
  };

  /** A failed start: Retries records it and its backoff says when the next starts; exhausted, the run fails. */
  const retry = async (runId: string, request: AgentRunRequest, detail: string): Promise<void> => {
    const runsRepo = runs();
    if (!runsRepo.retries?.recordAttempt) return finish(runId, 'failed', detail);
    let attempt: Values;
    try {
      attempt = await runsRepo.retries.recordAttempt(runId, { failure: 'sandbox', detail: { message: detail.slice(0, 500) } }, { caller: dispatcher });
    } catch (err) {
      return finish(runId, 'failed', `${detail}; ${err instanceof Error ? err.message : String(err)}`);
    }
    if (attempt.exhausted) return finish(runId, 'failed', detail);
    await moveRun(orm, runId, 'queued');
    await leaseCall(runId, 'release');
    const at = typeof attempt.retryAt === 'number' ? attempt.retryAt : now().getTime();
    const delaySeconds = Math.max(0, Math.ceil((at - now().getTime()) / 1000));
    await orm.transaction((tx: any) => enqueue(tx, options.requests, { ...request, step: 'start', run: runId }, delaySeconds > 0 ? { delaySeconds } : undefined));
  };

  const taskOf = (run: Values, definition: Values): string => {
    const input = (run.input as Values | null) ?? {};
    if (typeof input.task === 'string') return input.task;
    const message = input.message as Values | undefined;
    if (message && typeof message.body === 'string') return message.body;
    return String(definition.task ?? definition.purpose ?? `Work on ${run.triggerTable ?? 'the task'} ${run.triggerKey ?? ''}`.trim());
  };

  const watch = async (runId: string): Promise<void> => {
    const run = await runs().findOne({ id: { eq: runId } });
    if (!run || !['running', 'waiting'].includes(String(run.status)) || !run.instance || !run.sandboxSlot || !options.providers) return;
    const provider = await options.providers.sandbox(String(run.sandboxSlot));
    const status = await provider.status(String(run.instance));
    if (status.state === 'running' || status.state === 'starting') {
      await leaseCall(runId, 'heartbeat');
      await orm.transaction((tx: any) => enqueue(tx, options.requests, { step: 'watch', run: runId }, { delaySeconds: watchSeconds }));
      return;
    }
    const reported = run.outcome as string | null;
    if (status.state === 'succeeded') return finish(runId, reported ?? 'done');
    if (status.state === 'cancelled') return finish(runId, 'cancelled', 'the instance was cancelled');
    return finish(runId, 'failed', status.detail ?? `the instance exited ${status.exitCode ?? ''}`.trim());
  };

  const followUp = async (request: AgentRunRequest): Promise<void> => {
    const ask = request.ask;
    if (!ask || !request.run) return;
    const run = await runs().findOne({ id: { eq: request.run } });
    if (!run) return;
    const row = { table: ask.table, key: ask.key };
    const answered = await orm.transaction((tx: any) => threadOf().answered(tx, { row, message: ask.message, run: String(run.id) }));
    if (answered) return;
    if (options.followUp) await orm.transaction((tx: any) => options.followUp!(tx, { run, row, question: ask.question }));
    await append(orm, String(run.id), [{ kind: 'note', content: `nobody answered by the deadline: ${ask.question}` }]);
  };

  const handle = async (message: AgentRunRequest): Promise<void> => {
    switch (message.step ?? 'start') {
      case 'watch':
        return watch(String(message.run));
      case 'followUp':
        return followUp(message);
      default:
        return start(message);
    }
  };

  const workerQueue = (pool: QueuePool): WorkerQueue => {
    const queue = postgresQueue(pool, options.requests);
    return {
      name: queue.name,
      leaseSeconds: queue.leaseSeconds,
      claim: async (n, leaseSeconds) =>
        (await queue.claim(n, leaseSeconds)).map((claim): WorkerMessage => ({ id: claim.id, token: claim.token, attempt: claim.attempt, handle: () => handle(claim.message as AgentRunRequest) })),
      extend: (id, token, leaseSeconds) => queue.extend(id, token, leaseSeconds),
      complete: (id, token) => queue.complete(id, token),
      fail: (id, token, cause) => queue.fail(id, token, cause),
      release: (id, token) => queue.release(id, token),
      prune: async limit => (await queue.prune(limit)) + (await prune(limit)),
      slot: slots => queue.slot(slots),
      nextDue: () => queue.nextDue(),
    };
  };

  const request = async (tx: any, input: AgentRunRequest, enqueueOptions?: { readonly delaySeconds?: number }): Promise<void> => {
    if (input.requestedBy?.kind === 'agent') {
      throw new AgentsError(403, 'agents_cannot_request', 'an agent run may not start a run: only the dispatcher starts one (D158)');
    }
    if (input.trigger === 'inbound' && (!input.party || !input.grant)) {
      throw new AgentsError(400, 'invalid_request', "an inbound message's request names its party and its grant");
    }
    await enqueue(tx, options.requests, { step: 'start', ...input }, enqueueOptions?.delaySeconds ? { delaySeconds: enqueueOptions.delaySeconds } : undefined);
  };

  // ---- the run's principal ----

  const permissionsOf = async (ref: { kind: string; subject: string }): Promise<string[]> => {
    const db = sys(orm);
    if (!rolesMember) return [];
    const out = new Set<string>();
    if (ref.kind === 'user') {
      const grantsMember = tableMember('UserRoleGrant');
      if (!grantsMember) return [];
      for (const grant of (await repo(db, grantsMember).findMany({ userId: { eq: ref.subject } }, { limit: 1000, count: false })).rows) {
        const role = await repo(db, rolesMember).findOne({ id: { eq: grant.roleId } });
        for (const p of (role?.permissions as string[] | undefined) ?? []) out.add(p);
      }
    } else {
      for (const grant of await activeGrants(db, { holder: ref.subject, now: now() })) {
        const role = await repo(db, rolesMember).findOne({ name: { eq: grant.role } });
        for (const p of (role?.permissions as string[] | undefined) ?? []) out.add(p);
      }
    }
    return [...out];
  };

  /** The agent's permissions narrowed to those whom it acts for holds: each the two share, the narrower of a pair. */
  const narrow = (agent: readonly string[], actor: readonly string[]): string[] => {
    const out = new Set<string>();
    for (const p of agent) if (actor.some(q => covers(q, p))) out.add(p);
    for (const q of actor) if (agent.some(p => covers(p, q))) out.add(q);
    return [...out];
  };

  const grants = grantAuthenticator({ orm, now });

  const principalOf = async (req: Request): Promise<Principal | null> => {
    const header = req.headers.get('authorization') ?? '';
    if (!header.startsWith('Bearer grant_')) return null;
    let base: Principal | null;
    try {
      base = await grants({ headers: req.headers } as unknown as RequestContext);
    } catch {
      // An unknown, revoked or ended grant: no running run holds it.
      return null;
    }
    if (!base || base.kind !== 'agent') return null;
    const run = await runs().findOne({ id: { eq: base.subject } });
    if (!run || !['running', 'waiting'].includes(String(run.status))) return null;
    const reaches: GrantReach[] = [];
    const db = sys(orm);
    for (const grant of await activeGrants(db, { holder: base.subject, now: now() })) {
      const role = rolesMember ? await repo(db, rolesMember).findOne({ name: { eq: grant.role } }) : null;
      reaches.push({ id: String(grant.id), targetType: String(grant.targetType), targetId: String(grant.targetId), reaches: reachesOf(role?.reaches as string[] | undefined) });
    }
    const actingFor = run.actingForKind && run.actingFor ? { kind: run.actingForKind as 'user' | 'party', subject: String(run.actingFor) } : undefined;
    const permissions = actingFor ? narrow(base.permissions, await permissionsOf(actingFor)) : [...base.permissions];
    return {
      subject: base.subject,
      kind: 'agent',
      permissions,
      grants: reaches,
      ...(actingFor ? { actingFor } : {}),
      ...(base.credential ? { credential: base.credential } : {}),
      claims: { run: String(run.id), agent: String(run.agent), definitionVersion: Number(run.definitionVersion) },
    };
  };

  const authenticator = (inner?: Authenticator): Authenticator =>
    Object.assign(
      async (ctx: RequestContext): Promise<Principal | null> => {
        const header = ctx.headers.get('authorization') ?? '';
        if (!header.startsWith('Bearer grant_')) return inner ? inner(ctx) : null;
        const principal = await principalOf(new Request('http://agent.invalid/', { headers: ctx.headers }));
        if (!principal) {
          // A grant's token that is no running run's is any other grant's.
          return grants(ctx);
        }
        return principal;
      },
      { readsAuthorization: true }
    );

  const runOf = async (principal: Principal): Promise<Values> => {
    const id = (principal.claims?.run as string | undefined) ?? principal.subject;
    const run = await runs().findOne({ id: { eq: id } });
    if (!run) throw new AgentsError(401, 'unauthorized', 'no such run');
    return run;
  };
  const callerOf = (principal: Principal): CoreCaller => ({ subject: principal.subject, permissions: principal.permissions });

  // ---- autonomy ----

  const policyMode = async (agent: string, operation: string, cls: OperationClass): Promise<{ mode?: Autonomy; permission?: string }> => {
    const policies = repo(self(orm), parts.policies);
    const rows = [
      ...(await policies.findMany({ agent: { eq: agent } }, { limit: 500, count: false })).rows,
      ...(await policies.findMany({ agent: { isNull: true } }, { limit: 500, count: false })).rows,
    ];
    const score = (row: Values): number => (row.agent ? 4 : 0) + (row.operation === operation ? 2 : row.operation ? -100 : row.operationClass === cls ? 1 : -100);
    const best = rows.filter(r => score(r) >= 0).sort((a, b) => score(b) - score(a))[0];
    return best ? { mode: best.mode as Autonomy, ...(best.permission ? { permission: String(best.permission) } : {}) } : {};
  };

  const askApproval = async (run: Values, operation: string, cls: OperationClass, params: unknown, permission: string, summary: string): Promise<Values> => {
    const approvals = repo(self(orm), parts.approvals);
    const hash = paramsHash(operation, params);
    return orm.transaction(async (tx: any) => {
      const created = await repo(self(tx), parts.approvals).createOne({
        agent: run.agent,
        run: run.id,
        operation,
        operationClass: cls,
        params: redact(params),
        paramsHash: hash,
        summary: redactText(summary),
        permission,
        state: 'pending',
        expiresAt: new Date(now().getTime() + approvalTtl * 1000),
      });
      await raise(tx, (approvals.def as { name: string }).name, String(created.id), 'approval.requested', { operation, summary }, 'urgent');
      await runs(tx).updateOne(run.id, { approvals: Number(run.approvals ?? 0) + 1 });
      return created;
    });
  };

  const gate = async (principal: Principal, operation: string, args: Readonly<Record<string, unknown>>, gateOptions: { readonly class?: OperationClass; readonly mode?: Autonomy } = {}): Promise<GateOutcome> => {
    const run = await runOf(principal);
    const cls = gateOptions.class ?? 'internalWrite';
    const policy = await policyMode(String(run.agent), operation, cls);
    let mode: Autonomy = policy.mode ?? gateOptions.mode ?? defaultAutonomy(cls);
    if (cls === 'moneyMovement') mode = 'ask';
    if (mode !== 'ask') return { run: true, mode };
    const approvals = repo(self(orm), parts.approvals);
    const hash = paramsHash(operation, args);
    const open = (await approvals.findMany({ run: { eq: run.id }, operation: { eq: operation }, state: { in: ['pending', 'approved'] } }, { limit: 100, count: false })).rows;
    const live = open.filter(a => !a.expiresAt || new Date(a.expiresAt).getTime() > now().getTime());
    // A changed parameter: every open approval of another hash no longer holds.
    for (const other of live.filter(a => a.paramsHash !== hash)) await approvals.updateOne(other.id, { state: 'superseded' });
    const same = live.find(a => a.paramsHash === hash);
    if (same?.state === 'approved') {
      await approvals.updateOne(same.id, { state: 'used', usedAt: now() });
      if (run.status === 'waiting') await moveRun(orm, String(run.id), 'running');
      return { run: true, mode, approval: String(same.id) };
    }
    const denied = (await approvals.findMany({ run: { eq: run.id }, operation: { eq: operation }, paramsHash: { eq: hash }, state: { eq: 'denied' } }, { limit: 1, count: false })).rows[0];
    if (denied) return { run: false, status: 403, code: 'approval_denied', detail: `${operation} was denied`, approval: String(denied.id) };
    const approval = same ?? (await askApproval(run, operation, cls, args, policy.permission ?? AGENT_PERMISSIONS.approve, `${run.agentName} asks to run ${operation} with ${canonicalJson(args)}`));
    if (run.status === 'running') await moveRun(orm, String(run.id), 'waiting');
    return { run: false, status: 428, code: 'approval_required', detail: `${operation} runs once someone holding ${approval.permission} approves it: approval ${approval.id}`, approval: String(approval.id) };
  };

  const decide = async (principal: Principal, id: string, state: 'approved' | 'denied', decideOptions: { readonly tx?: any; readonly via?: string } = {}): Promise<void> => {
    const db = decideOptions.tx ?? orm;
    const approvals = repo(self(db), parts.approvals);
    const approval = await approvals.findOne({ id: { eq: id } });
    if (!approval) throw new AgentsError(404, 'not_found', 'no such approval');
    if (!principal.permissions.some(p => covers(p, String(approval.permission)))) throw new AgentsError(403, 'forbidden', `approving needs ${approval.permission}`);
    if (principal.kind === 'agent') throw new AgentsError(403, 'forbidden', 'an agent run may not decide an approval');
    if (approval.state !== 'pending') throw new AgentsError(409, 'not_pending', `the approval is ${approval.state}`);
    await approvals.updateOne(id, { state, decidedBy: principal.subject, decidedVia: decideOptions.via ?? 'api', decidedAt: now() });
    if (approval.operation === 'agents.overBudget' && approval.run) {
      if (state === 'approved') await enqueue(db, options.requests, { step: 'start', run: String(approval.run) });
      else await enqueue(db, options.requests, { step: 'watch', run: String(approval.run) });
      if (state === 'denied') await finish(String(approval.run), 'refused', 'its approval to run past the budget was denied');
    }
  };

  const replyActions = {
    approve: {
      permission: AGENT_PERMISSIONS.approve,
      describe: 'Approved',
      async run(ctx) {
        await decide(ctx.principal, String(ctx.notice.subjectKey), 'approved', { tx: ctx.tx, via: ctx.channel });
        return { approval: ctx.notice.subjectKey };
      },
    } satisfies ReplyAction,
    deny: {
      permission: AGENT_PERMISSIONS.approve,
      describe: 'Denied',
      async run(ctx) {
        await decide(ctx.principal, String(ctx.notice.subjectKey), 'denied', { tx: ctx.tx, via: ctx.channel });
        return { approval: ctx.notice.subjectKey };
      },
    } satisfies ReplyAction,
  };

  // ---- tools ----

  const allowed = (definition: Values, name: string): boolean => {
    const list = definition.tools as string[] | null | undefined;
    return !list || list.includes(name);
  };

  const threadOf = (): AskThread =>
    options.thread ?? {
      async post(tx, input) {
        const member = tableMember(input.row.table);
        const repository = member ? (tx[member] as RepositoryShape) : undefined;
        if (!repository?.comments?.post) throw new AgentsError(400, 'no_thread', `${input.row.table} keeps no Comments to ask in`);
        const posted = await repository.comments.post(input.row.key, { body: input.question, ...(input.principal ? { mentions: [input.principal] } : {}) }, { caller: input.caller });
        return String(posted?.id ?? posted?.message ?? '');
      },
      async answered(tx, input) {
        const member = tableMember(input.row.table);
        const repository = member ? (sys(tx)[member] as RepositoryShape) : undefined;
        if (!repository?.comments?.listMessages) return false;
        const message = Number(input.message);
        const listed = await repository.comments.listMessages(input.row.key, { thread: message, limit: 100 }, { caller: dispatcher });
        const items = (listed?.items as Values[] | undefined) ?? [];
        return items.some(m => Number(m.id) !== message && String(m.author) !== input.run);
      },
    };

  const mcpTools = async (principal: Principal): Promise<McpToolSet> => {
    const run = await runOf(principal);
    const definition = (await defs().findOne({ id: { eq: run.agent } }))!;
    const caller = callerOf(principal);
    const offered: { tool: McpTool; call: (args: Readonly<Record<string, unknown>>) => Promise<McpCallOutcome> }[] = [];
    const problem = (status: number, code: string, detail: string, extra: Values = {}): McpCallOutcome => ({
      problem: { type: 'about:blank', title: code, status, detail, code, ...extra },
    });
    const record = async (name: string, args: unknown, outcome: string, counts: Values): Promise<void> => {
      await orm.transaction(async (tx: any) => {
        const current = await runs(tx).findOne({ id: { eq: run.id } });
        const update: Values = {};
        for (const [field, by] of Object.entries(counts)) update[field] = Number(current?.[field] ?? 0) + Number(by);
        if (Object.keys(update).length > 0) await runs(tx).updateOne(run.id, update);
      });
      await append(orm, String(run.id), [{ kind: 'call', tool: name, args, outcome }]);
    };
    const counter = (cls: OperationClass): string => (cls === 'read' ? 'reads' : cls === 'messageParty' ? 'messagesSent' : 'writes');
    for (const [name, tool] of Object.entries(tools)) {
      if (!allowed(definition, name)) continue;
      if (tool.permission && !principal.permissions.some(p => covers(p, tool.permission!))) continue;
      offered.push({
        tool: { name, description: tool.description, inputSchema: tool.inputSchema, ...(tool.untrusted ? { _meta: { untrusted: true } } : {}) },
        call: async args => {
          const gated = await gate(principal, name, args, { class: tool.class, ...(tool.mode ? { mode: tool.mode } : {}) });
          if (!gated.run) {
            await record(name, args, gated.code, {});
            return problem(gated.status, gated.code, gated.detail, gated.approval ? { approval: gated.approval } : {});
          }
          try {
            const result = await withCaller(principal, () =>
              orm.as(principal).withBehaviorCaller(caller).transaction((tx: any) => tool.run({ tx, principal, run, args, caller }))
            );
            await record(name, args, 'done', { [counter(tool.class)]: 1, ...(tool.untrusted ? { untrusted: 1 } : {}) });
            if (gated.mode === 'tell') await orm.transaction((tx: any) => raise(tx, runsDef.name, String(run.id), 'agent.acted', { tool: name }));
            return { result: tool.untrusted ? { untrusted: true, content: result } : result };
          } catch (err) {
            const status = typeof (err as { status?: unknown }).status === 'number' ? Number((err as { status: number }).status) : err instanceof Error && err.name === 'AccessRefusedError' ? 403 : err instanceof Error && err.name === 'NotFoundError' ? 404 : 500;
            const detail = err instanceof Error ? err.message : String(err);
            await record(name, args, `failed ${status}`, {});
            return problem(status, status === 404 ? 'not_found' : status === 403 ? 'forbidden' : 'failed', redactText(detail));
          }
        },
      });
    }
    if (options.search && allowed(definition, 'search')) {
      const searchOptions = options.search === true ? {} : options.search;
      offered.push({
        tool: { name: searchMcpTool.name, title: searchMcpTool.title, description: searchMcpTool.description, inputSchema: searchMcpTool.inputSchema, annotations: searchMcpTool.annotations },
        call: async args => {
          const answer = await callSearchTool(orm, principal, args, searchOptions);
          await record('search', args, 'problem' in answer ? 'refused' : 'done', 'problem' in answer ? {} : { reads: 1 });
          return 'problem' in answer ? problem(answer.problem.status, answer.problem.title, answer.problem.detail) : { result: answer.result };
        },
      });
    }
    if (allowed(definition, 'ask_principal')) {
      offered.push({
        tool: {
          name: 'ask_principal',
          description: "Asks a principal a question in a target row's thread; if nobody answers by the deadline, a follow-up row is made.",
          inputSchema: {
            type: 'object',
            additionalProperties: false,
            required: ['table', 'key', 'question'],
            properties: { table: { type: 'string' }, key: { type: 'string' }, question: { type: 'string', minLength: 1 }, principal: { type: 'string' }, deadlineSeconds: { type: 'integer', minimum: 1 } },
          },
        },
        call: async args => {
          const row = { table: String(args.table), key: String(args.key) };
          if (!targetsOf(run).some(t => t.table === row.table && t.key === row.key)) {
            await record('ask_principal', args, 'forbidden', {});
            return problem(403, 'forbidden', 'a run asks only in the thread of one of its target rows');
          }
          const question = String(args.question);
          const message = await withCaller(principal, () =>
            orm.as(principal).withBehaviorCaller(caller).transaction((tx: any) => threadOf().post(tx, { row, question, ...(typeof args.principal === 'string' ? { principal: args.principal } : {}), caller }))
          );
          const delaySeconds = typeof args.deadlineSeconds === 'number' ? args.deadlineSeconds : askSeconds;
          await orm.transaction((tx: any) => enqueue(tx, options.requests, { step: 'followUp', run: String(run.id), ask: { table: row.table, key: row.key, message, question } }, { delaySeconds }));
          await record('ask_principal', args, 'asked', { messagesSent: 1 });
          return { result: { asked: true, message, deadlineSeconds: delaySeconds } };
        },
      });
    }
    return {
      list: () => offered.map(o => o.tool),
      call: async (name, args) => {
        const entry = offered.find(o => o.tool.name === name);
        if (!entry) return problem(404, 'not_found', `no tool ${name}`);
        return entry.call(args);
      },
    };
  };

  /** The tools offered to the model: the run's MCP tools and the sandbox's own. */
  const modelTools = async (principal: Principal, definition: Values): Promise<{ list: ModelTool[]; untrusted: Set<string> }> => {
    const set = await mcpTools(principal);
    const listed = await set.list();
    const untrusted = new Set<string>();
    const list: ModelTool[] = listed.map(t => {
      if ((t._meta as Values | undefined)?.untrusted) untrusted.add(t.name);
      return { name: t.name, description: t.description ?? t.name, inputSchema: t.inputSchema as Record<string, unknown> };
    });
    for (const tool of sandboxTools) {
      if (!allowed(definition, tool.name)) continue;
      if (tool.untrusted) untrusted.add(tool.name);
      list.push({ name: tool.name, description: tool.description, inputSchema: tool.inputSchema });
    }
    return { list, untrusted };
  };

  // ---- the agent routes ----

  const json = (status: number, body: unknown): Response => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
  const refusal = (status: number, code: string, detail: string): Response => json(status, { type: 'about:blank', title: code, status, code, detail });

  const turn = async (principal: Principal, body: Values): Promise<Response> => {
    const run = await runOf(principal);
    if (!['running', 'waiting'].includes(String(run.status))) return refusal(409, 'run_ended', 'the run has ended');
    const agent = String(run.agent);
    const stand = await standing(orm, agent);
    if (stand.paused) {
      await cancel(String(run.id), 'paused', `the agent is paused (${stand.reason})`);
      return refusal(409, 'agent_paused', 'the agent is paused');
    }
    if (stand.agentCapped) {
      await setPaused(agent, true, 'budget', 'budget');
      return refusal(409, 'agent_paused', "the agent reached its budget's cap");
    }
    const definition = (await defs().findOne({ id: { eq: run.agent } }))!;
    const limits = (definition.limits as Values | null) ?? {};
    if (limits.maxTurns !== undefined && Number(run.turns ?? 0) >= Number(limits.maxTurns)) return refusal(409, 'max_turns', 'the run took its turns');
    const { list, untrusted } = await modelTools(principal, definition);
    const recorded = ((run.transcript as TranscriptEntry[] | null) ?? []);
    const untrustedCalls = new Set<string>(((run.untrustedCalls as string[] | null) ?? []).map(String));
    const incoming = ((body.messages as ModelMessage[] | undefined) ?? []).filter(m => m.role === 'tool' || m.role === 'user').map(m => ({
      kind: 'message' as const,
      role: m.role,
      content: String(m.content ?? ''),
      ...(m.toolCallId ? { toolCallId: String(m.toolCallId) } : {}),
      // A tool result of a call marked untrusted, and anything a runner sends as a user, is data.
      ...((m.toolCallId && untrustedCalls.has(String(m.toolCallId))) || m.role === 'user' ? { untrusted: true } : {}),
    }));
    await append(orm, String(run.id), incoming);
    const outside = incoming.filter(m => m.untrusted && m.role === 'tool').length;
    if (outside > 0) await runs().updateOne(run.id, { untrusted: Number(run.untrusted ?? 0) + outside });
    const history = [...recorded, ...incoming.map(e => redact(e))];
    const messages: ModelMessage[] = [];
    if (definition.instructions) messages.push({ role: 'system', content: String(definition.instructions) });
    for (const entry of history) {
      if (entry.kind === 'task') messages.push({ role: 'user', content: String(entry.content ?? ''), ...(entry.untrusted ? { untrusted: true } : {}) });
      else if (entry.kind === 'message') messages.push({ role: entry.role ?? 'tool', content: String(entry.content ?? ''), ...(entry.toolCallId ? { toolCallId: entry.toolCallId } : {}), ...(entry.untrusted ? { untrusted: true } : {}) });
      else if (entry.kind === 'completion') messages.push({ role: 'assistant', content: String(entry.content ?? '') });
    }
    const { provider, slot } = await modelOf(definition);
    let completion: ModelCompletion;
    try {
      completion = await provider.complete({ ...(definition.model ? { model: String(definition.model) } : {}), messages, tools: list });
    } catch (err) {
      return refusal(502, 'model_failed', redactText(err instanceof Error ? err.message : String(err)));
    }
    for (const call of completion.toolCalls) if (untrusted.has(call.name)) untrustedCalls.add(call.id);
    const spent = costOf(completion.usage);
    await orm.transaction(async (tx: any) => {
      const current = (await runs(tx).findOne({ id: { eq: run.id } }))!;
      await runs(tx).updateOne(run.id, {
        turns: Number(current.turns ?? 0) + 1,
        inputTokens: Number(current.inputTokens ?? 0) + completion.usage.inputTokens,
        outputTokens: Number(current.outputTokens ?? 0) + completion.usage.outputTokens,
        cost: Number(current.cost ?? 0) + spent,
        untrustedCalls: [...untrustedCalls],
        modelSlot: slot,
      });
    });
    await append(orm, String(run.id), [{ kind: 'completion', role: 'assistant', content: completion.content, toolCalls: completion.toolCalls.map(c => ({ id: c.id, name: c.name, input: c.input })) }]);
    await meterUsage(agent, spent);
    return json(200, { content: redactText(completion.content), toolCalls: redact(completion.toolCalls), stop: completion.stop, usage: completion.usage });
  };

  const routes = (routeOptions: { readonly authenticate?: (request: Request) => Promise<Principal | null> } = {}) => async (req: Request): Promise<Response | null> => {
    const url = new URL(req.url);
    if (!url.pathname.startsWith(`${base}/`)) return null;
    const rest = url.pathname.slice(base.length + 1);
    const approvalMatch = /^approvals\/([^/]+)\/(approve|deny)$/u.exec(rest);
    if (approvalMatch) {
      if (req.method !== 'POST') return refusal(405, 'method_not_allowed', 'a decision is a POST');
      const who = routeOptions.authenticate ? await routeOptions.authenticate(req) : null;
      if (!who) return refusal(401, 'unauthorized', 'Authentication required');
      try {
        await decide(who, decodeURIComponent(approvalMatch[1]!), approvalMatch[2] === 'approve' ? 'approved' : 'denied', { via: 'api' });
        return json(200, { state: approvalMatch[2] === 'approve' ? 'approved' : 'denied' });
      } catch (err) {
        if (err instanceof AgentsError) return refusal(err.status, err.code, err.message);
        throw err;
      }
    }
    if (!['turn', 'mcp', 'credential', 'finish'].includes(rest)) return null;
    if (req.method !== 'POST') return refusal(405, 'method_not_allowed', `${rest} is a POST`);
    const principal = await principalOf(req);
    if (!principal) return refusal(401, 'unauthorized', 'no running run holds this token');
    if (rest === 'mcp') return serveMcp(req, await mcpTools(principal), { serverInfo: { name: 'agent-run', version: '1' }, caller: principal.subject });
    const body = (await req.json().catch(() => ({}))) as Values;
    if (rest === 'turn') return turn(principal, body);
    const run = await runOf(principal);
    if (rest === 'credential') {
      // What the sandbox reports of a credential it used: its reference and target, never its value.
      const use = { ref: String(body.ref ?? ''), provider: String(body.provider ?? ''), target: redact(body.target ?? null), usedAt: String(body.usedAt ?? now().toISOString()) };
      await orm.transaction(async (tx: any) => {
        const current = (await runs(tx).findOne({ id: { eq: run.id } }))!;
        await runs(tx).updateOne(run.id, { credentialUses: [...((current.credentialUses as unknown[] | null) ?? []), use] });
      });
      await append(orm, String(run.id), [{ kind: 'credential', ref: use.ref, content: `used ${use.ref} on ${JSON.stringify(use.target)}` }]);
      return json(200, { recorded: true });
    }
    // finish: the outcome the instance reports, kept for the watch that ends the run.
    const outcome = ['done', 'failed'].includes(String(body.outcome)) ? String(body.outcome) : 'done';
    await runs().updateOne(run.id, { outcome, ...(body.summary ? { detail: redactText(String(body.summary)).slice(0, 2000) } : {}) });
    if (body.summary) await append(orm, String(run.id), [{ kind: 'note', content: String(body.summary) }]);
    return json(200, { recorded: true });
  };

  // ---- administration ----

  const need = (principal: Principal, permission: string): void => {
    if (principal.kind === 'agent') throw new AgentsError(403, 'forbidden', 'an agent run may not administer agents');
    if (!principal.permissions.some(p => covers(p, permission))) throw new AgentsError(403, 'forbidden', `this needs ${permission}`);
  };

  const setPaused = async (agent: string | null, paused: boolean, reason: string | null, by: string | null): Promise<void> => {
    const control = await controlOf(orm, agent);
    await repo(self(orm), parts.controls).updateOne(control.id, { paused, pausedReason: paused ? reason : null, pausedBy: paused ? by : null, pausedAt: paused ? now() : null });
    if (!paused) return;
    const filter: Values = { status: { in: ['queued', 'running', 'waiting'] } };
    if (agent !== null) filter.agent = { eq: agent };
    for (const run of (await runs().findMany(filter, { limit: 10000, count: false })).rows) {
      await cancel(String(run.id), reason === 'budget' ? 'budget' : 'paused', reason === 'budget' ? "the agent reached its budget's cap" : `the ${agent === null ? 'agents are' : 'agent is'} paused`);
    }
  };

  const pause = async (principal: Principal, agent: string | null, reason = 'stopped'): Promise<void> => {
    need(principal, AGENT_PERMISSIONS.pause);
    const id = agent === null ? null : String((await findDefinition(agent))?.id ?? agent);
    await setPaused(id, true, reason, principal.subject);
  };
  const resume = async (principal: Principal, agent: string | null): Promise<void> => {
    need(principal, AGENT_PERMISSIONS.pause);
    const id = agent === null ? null : String((await findDefinition(agent))?.id ?? agent);
    await setPaused(id, false, null, null);
  };

  const setPolicy: Agents['setPolicy'] = async (principal, policy) => {
    need(principal, AGENT_PERMISSIONS.autonomy);
    if (!['auto', 'tell', 'ask'].includes(policy.mode)) throw new AgentsError(400, 'invalid_mode', `mode ${policy.mode} is none of auto, tell and ask`);
    if (!policy.operation === !policy.operationClass) throw new AgentsError(400, 'invalid_policy', 'a policy names an operation or a class, one of them');
    const cls = policy.operationClass ?? (policy.operation ? tools[policy.operation]?.class : undefined);
    if (cls === 'moneyMovement' && policy.mode !== 'ask') throw new AgentsError(400, 'money_asks', 'an operation that moves money is ask and nothing else: an agent may only ask to move money (D158)');
    const agentId = policy.agent ? String((await findDefinition(policy.agent))?.id ?? policy.agent) : null;
    return repo(self(orm), parts.policies).createOne({ agent: agentId, operation: policy.operation ?? null, operationClass: policy.operationClass ?? null, mode: policy.mode, permission: policy.permission ?? null });
  };

  const setBudget: Agents['setBudget'] = async (principal, agent, budget) => {
    need(principal, AGENT_PERMISSIONS.budgets);
    const id = agent === null ? null : String((await findDefinition(agent))?.id ?? agent);
    const control = await controlOf(orm, id);
    // A Budget's limit changes only for a caller holding its limitPermission: the administrator's own.
    const as = sys(orm).withBehaviorCaller(callerOf(principal));
    await repo(as, parts.controls).updateOne(control.id, { budgetLimit: budget.limit, ...(budget.warnAt !== undefined ? { warnAt: budget.warnAt } : {}) });
    // Every agent's limit is the limit of the budget their meters spend within.
    if (id === null && totalRelation && control[totalRelation.column] !== null && control[totalRelation.column] !== undefined) {
      await repo(as, totalRelation.target).updateOne(control[totalRelation.column], { budgetLimit: budget.limit });
    }
  };

  const define: Agents['define'] = async (principal, input) => {
    need(principal, AGENT_PERMISSIONS.define);
    const existing = typeof input.name === 'string' ? await defs().findOne({ name: { eq: input.name } }) : null;
    const credentialsChanged = input.credentials !== undefined && canonicalJson(input.credentials ?? null) !== canonicalJson(existing?.credentials ?? null);
    if (credentialsChanged) need(principal, AGENT_PERMISSIONS.credentials);
    for (const ref of (input.credentials as unknown[] | null | undefined) ?? []) {
      const text = typeof ref === 'string' ? ref : (ref as Values)?.ref;
      if (typeof text !== 'string' || text.length === 0) throw new AgentsError(400, 'invalid_credentials', 'a credential is named by its reference alone');
    }
    const written = existing ? await defs().updateOne(existing.id, input as Values) : await defs().createOne(input as Values);
    await controlOf(orm, String(written.id));
    return written;
  };

  const transcript = async (runId: string): Promise<TranscriptEntry[]> => {
    const run = await runs().findOne({ id: { eq: runId } });
    if (!run) return [];
    if (run.transcriptObject && options.transcripts && !run.transcriptPrunedAt) {
      const read = (await options.transcripts.get(String(run.transcriptObject))) as { body?: ReadableStream<Uint8Array>; text?: () => Promise<string> };
      const text = typeof read.text === 'function' ? await read.text() : await new Response(read.body!).text();
      return JSON.parse(text) as TranscriptEntry[];
    }
    return (run.transcript as TranscriptEntry[] | null) ?? [];
  };

  const prune = async (limit: number): Promise<number> => {
    if (!options.transcripts) return 0;
    const before = new Date(now().getTime() - retentionDays * 86_400_000);
    const old = (await runs().findMany({ endedAt: { lt: before }, transcriptObject: { isNull: false }, transcriptPrunedAt: { isNull: true } }, { limit, count: false })).rows;
    for (const run of old) {
      await options.transcripts.delete(String(run.transcriptObject));
      await runs().updateOne(run.id, { transcriptPrunedAt: now() });
    }
    return old.length;
  };

  const mcpGate: McpCallGate = async (ctx, tool, args) => {
    const principal = ctx.principal;
    if (!principal || principal.kind !== 'agent' || !principal.claims?.run) return undefined;
    const gated = await gate(principal, tool.name, args, { class: tool.autonomy?.class ?? 'internalWrite', ...(tool.autonomy?.mode ? { mode: tool.autonomy.mode } : {}) });
    if (gated.run) return undefined;
    return { type: 'about:blank', title: gated.code, status: gated.status, detail: gated.detail, code: gated.code, ...(gated.approval ? { approval: gated.approval } : {}) };
  };

  return {
    request,
    handle,
    mcpGate,
    workerQueue,
    authenticator,
    principalOf,
    routes,
    mcpTools,
    gate,
    approve: (principal, id, o) => decide(principal, id, 'approved', o),
    deny: (principal, id, o) => decide(principal, id, 'denied', o),
    replyActions,
    pause,
    resume,
    setPolicy,
    setBudget,
    define,
    cancel,
    transcript,
    prune,
    notifierAgents: { request: (tx, input) => request(tx, input) },
  };
}
