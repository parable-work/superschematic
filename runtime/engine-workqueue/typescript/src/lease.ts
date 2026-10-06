/*
Lease, an exclusive, time-bounded lease on an instance (D16), held by one
principal and numbered by a fencing token. The token is a per-instance
integer that starts at 0 and advances at every acquire, every release and
every applied expiry, so (instance, token) names one lease. A caller
presents it as Lease's precondition (`preconditions: { Lease: { token } }`),
the one way any write presents it: the guard refuses a write that
presents another token, whoever calls (token_stale), so a process of the
holder's principal that lost its lease cannot write once a sibling holds
a new one. A heartbeat, an acknowledgement and the holder's release need
the token; with requireToken, so does every other write while a lease is
active, the holder's own included. The token is no capability: the lease
field shows it to every reader, and the guard still checks who calls. It
fences a principal's processes from each other, not principals.

A lease is active while it is held, before its expiry time and within its
longest hold since acquire (maxHoldMs, or the instance's own maxHoldField).
A lease past either has lapsed: it gives its holder nothing, so the
holder's heartbeat, release, acknowledgement and writes are refused, and
only its expiry can follow. expire applies the expiry, and anyone who may
write the instance may call it; acquire applies it first over a lapsed
lease; and the expire schedule, which the engine's runner runs every
sweepMs as its principal, calls it on every lapsed lease of the schema,
found through the lease columns across the schema (sql.instances()) and
their index. A principal with overridePermission expires a given holder's
lease at once, active or not (expire with holder), and every lease that
holder has on the schema (expireHolder), for a holder that is gone;
notRenewedAfter spares an active lease acquired or renewed after a time,
whose own heartbeats show the process holding it alive, and expireHolder
returns the ids it expired. An
expiry clears the holder, advances the token, counts the expiry unless
the instance is in a terminal state of its Workflow (a holder that
finished and died before releasing has not failed), and moves the status
with onExpiry, or escalate at the expiry that reaches maxExpiries, only
from the config's from states and only through Workflow's transition,
whose guards run. A transition a guard vetoes leaves the status as it is
and the lease expired. Once the instance has had maxExpiries expiries, it
cannot be leased until a principal with overridePermission resets them.
A release applies onExpiry too, without counting an expiry, so a holder
that hands work back leaves it where it can be taken again; a release
with abandon is the holder giving the work up as failed, and counts as
an expiry, escalate included, so a worker that keeps taking and dropping
an instance reaches maxExpiries. The lease field's ended says how the
last lease ended: release, abandon, or the expiry's reason, ttl (its
holder stopped renewing it), maxHold (it reached its longest hold) or
holder (it was active and expired by its holder's name). acquire clears
it, so every end's event carries it.

The guard keeps a lease exclusive. While a lease is active it refuses an
update, a delete and every writing operation of another behavior by any
principal but the holder, except an operation the config exempts and a
principal with overridePermission; with requireToken, it refuses the
holder's too unless they present the token. A read-only operation passes, and so
does a request a behavior's own code makes (call() or update(), which
name it as caller): the operation that made it was asked already. Queue's
refresh passes too: it only recomputes Queue's own copies of the
instance's facts, and a blocker's change invokes it as whoever changed
the blocker. Lease's own operations check their callers themselves, but
for direct, whose permission the guard asks. Nothing changes the field
that limits the hold (maxHoldField) while a lease is held, so a holder
cannot extend its own hold. An operation another behavior's reference
hook invokes on a leased instance, as Dependencies' removeBlocker when a
blocker goes, runs as its caller and is refused like any other: a
deployment exempts the ones it wants through.

Directives are a holder's control channel. direct attaches one to the
current token, once per dedupeKey when it gives one; heartbeat returns
the ones not acknowledged, and acknowledge, or heartbeat's own
acknowledge, marks them handled. A principal's direct needs
directPermission (overridePermission when that is absent), which the
guard asks; another behavior of the type sends one through call(), as
the principal it runs for, with none, as a budget does when its usage
runs over. When the token advances, the directives
of the lease it ends are deleted, so none reaches the next holder.

Every refusal is a veto with a code the declaration lists: held_by_another,
held_by_caller, not_leased, not_holder, lapsed, token_stale,
token_required, max_expiries, hold_limit_fixed and not_configured.

configChange: any config may change. Lease can be added to a schema that
has instances, which start free at token 0, and cannot be removed from
one: the leases and directives its instances hold would stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  isTerminalState,
  type BehaviorScope,
  type ConfigTarget,
  type FrozenJSON,
  type GuardAnswer,
  type GuardRequest,
  type InstanceView,
  type OperationContext,
  type Row,
  type SqlValue,
  type WorkflowStates,
} from '@superschematic/engine';

import { DEFAULT_TTL_MS } from './defaults.js';
import declaration from './declarations/Lease.behavior.json' with { type: 'json' };

export { DEFAULT_TTL_MS } from './defaults.js';

/** How often the runner expires lapsed leases when the config gives no sweepMs. */
export const DEFAULT_SWEEP_MS = 5000;

/** The instances one query of the sweep or of expireHolder reads. */
const BATCH = 100;

/** The most leases one run of the expire schedule expires; the rest wait for the next. */
export const MAX_SWEEP = 1000;

/** Why expire applied an expiry. */
export type ExpiryReason = 'ttl' | 'maxHold' | 'holder';

/** How a lease ended: a release, an abandon, or an expiry for its reason. */
export type LeaseEnd = 'release' | 'abandon' | ExpiryReason;

/** A move of the status a lease's end makes: to transition, from one of from. */
export interface LeaseTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** Lease's config, parsed: the defaults filled in. */
export interface LeaseConfig {
  readonly ttlMs: number;
  readonly heartbeatMs: number;
  readonly sweepMs: number;
  readonly maxHoldMs?: number;
  readonly maxHoldField?: string;
  readonly onExpiry?: LeaseTransition;
  readonly maxExpiries?: number;
  readonly escalate?: LeaseTransition;
  /** The writing operations other principals may run while a lease is active, as `<Behavior>.<operation>`. */
  readonly exempt: readonly string[];
  /** Whether every write under an active lease presents the current token. */
  readonly requireToken: boolean;
  readonly acquirePermission?: string;
  readonly overridePermission?: string;
  readonly directPermission?: string;
}

/** The lease field. */
export interface LeaseRecord {
  /** The principal that holds the lease; null when it is free. */
  readonly holder: string | null;
  readonly token: number;
  readonly acquiredAt: number | null;
  /** When its holder last acquired or renewed it; null when free. */
  readonly renewedAt: number | null;
  /** When the lease stops being active: its expiry time, or its longest hold if that comes first; null when free. */
  readonly expiresAt: number | null;
  /** Whether it is held and neither expired nor past its longest hold. */
  readonly active: boolean;
  /** How many expiries the instance has had, abandons included. */
  readonly expiries: number;
  /** How and when the last lease ended; null while one is held, and before the first. */
  readonly ended: { readonly reason: LeaseEnd; readonly at: number } | null;
}

/** A directive, as heartbeat returns it. */
export interface DirectiveRecord {
  /** Its number in its lease, 1, 2, 3, .... */
  readonly id: number;
  readonly name: string;
  readonly data?: Record<string, unknown>;
  /** The key it was sent with, which sends it once per lease. */
  readonly dedupeKey?: string;
  readonly createdAt: number;
  readonly createdBy: string;
}

const NAME = 'Lease';

/** The lease's own columns on an instance. */
interface Held {
  readonly holder: string | null;
  readonly token: number;
  readonly acquiredAt: number | null;
  /** Its acquire or its last heartbeat; null for a lease held before the column was added, which reads acquiredAt. */
  readonly renewedAt: number | null;
  readonly expiresAt: number | null;
  /** The length the lease was acquired with, which each heartbeat renews it by. */
  readonly ttlMs: number | null;
  readonly expiries: number;
  readonly endedReason: LeaseEnd | null;
  readonly endedAt: number | null;
}

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

function held(view: InstanceView<LeaseConfig>): Held {
  const columns = view.columns.get();
  const number = (value: unknown): number | null => (value === null || value === undefined ? null : Number(value));
  return {
    holder: columns.holder === null || columns.holder === undefined ? null : String(columns.holder),
    token: Number(columns.token),
    acquiredAt: number(columns.acquired_at),
    renewedAt: number(columns.renewed_at),
    expiresAt: number(columns.expires_at),
    ttlMs: number(columns.ttl_ms),
    expiries: Number(columns.expiries),
    endedReason: columns.ended_reason === null || columns.ended_reason === undefined ? null : (String(columns.ended_reason) as LeaseEnd),
    endedAt: number(columns.ended_at),
  };
}

// holdLimit is the longest the instance's lease may be held after its
// acquire: the instance's maxHoldField when it holds a positive integer,
// else maxHoldMs; undefined for no limit.
function holdLimit(view: InstanceView<LeaseConfig>): number | undefined {
  const field = view.config.maxHoldField;
  if (field !== undefined) {
    const value = view.data[field];
    if (typeof value === 'number' && Number.isSafeInteger(value) && value > 0) {
      return value;
    }
  }
  return view.config.maxHoldMs;
}

// deadline is when a held lease stops being active.
function deadline(view: InstanceView<LeaseConfig>, lease: Held): number {
  const expires = lease.expiresAt ?? 0;
  const limit = holdLimit(view);
  return limit === undefined ? expires : Math.min(expires, (lease.acquiredAt ?? 0) + limit);
}

/** isActive is the one rule for whether a lease holds: held, before its expiry time and within its longest hold. */
function isActive(view: InstanceView<LeaseConfig>, lease: Held): boolean {
  return lease.holder !== null && view.now < deadline(view, lease);
}

// lapseReason is why a held lease stopped being active: its longest hold
// when that came no later than its expiry time, else its expiry time.
function lapseReason(view: InstanceView<LeaseConfig>, lease: Held): ExpiryReason {
  const limit = holdLimit(view);
  return limit !== undefined && (lease.acquiredAt ?? 0) + limit <= (lease.expiresAt ?? 0) ? 'maxHold' : 'ttl';
}

function time(ms: number): string {
  return new Date(ms).toISOString();
}

/** The codes Lease's vetoes carry, as its declaration lists them. */
type LeaseVeto =
  | 'held_by_another'
  | 'held_by_caller'
  | 'not_leased'
  | 'not_holder'
  | 'lapsed'
  | 'token_stale'
  | 'token_required'
  | 'max_expiries'
  | 'hold_limit_fixed'
  | 'not_configured';

function veto(reason: string, code: LeaseVeto, details?: Record<string, unknown>): GuardAnswer {
  return details === undefined ? { reason, code } : { reason, code, details };
}

function vetoed(view: InstanceView<unknown>, operation: string, reason: string, code: LeaseVeto, details?: Record<string, unknown>): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, details === undefined ? { reason, code } : { reason, code, details });
}

// presented is the token a request presents as Lease's precondition.
function presented(request: GuardRequest): number | undefined {
  const token = request.precondition?.token;
  return typeof token === 'number' ? token : undefined;
}

function stale(token: number, lease: Held): GuardAnswer {
  return veto(`token ${token} is stale: the lease is at token ${lease.token}`, 'token_stale', { token, current: lease.token });
}

// lapsed is the veto of a call that needs an active lease, its reason
// written for the time the lease lapsed.
function lapsed(view: InstanceView<LeaseConfig>, lease: Held, reason: (at: string) => string): GuardAnswer {
  const at = deadline(view, lease);
  return veto(reason(time(at)), 'lapsed', { expiredAt: at });
}

function forbidden(view: InstanceView<unknown>, what: string, permission: string): EngineError {
  return new EngineError('forbidden', `${view.principal.subject} may not ${what} ${view.schema} ${view.id}: it needs permission ${permission}`);
}

function overrides(scope: BehaviorScope<LeaseConfig>): boolean {
  const permission = scope.config.overridePermission;
  return permission !== undefined && scope.can(permission);
}

// heartbeatFor is the heartbeat interval of a lease acquired for ttl: the
// config's, scaled to a shorter ttl.
function heartbeatFor(config: LeaseConfig, ttl: number): number {
  return ttl === config.ttlMs ? config.heartbeatMs : Math.max(1, Math.floor((config.heartbeatMs * ttl) / config.ttlMs));
}

/** The instance's Workflow config and status, read as the caller; undefined without Workflow. */
interface Status {
  readonly flow: WorkflowStates;
  readonly status: string;
}

function statusOf(view: InstanceView<LeaseConfig>): Status | undefined {
  const flow = view.schemas.config(view.schema, 'Workflow') as WorkflowStates | undefined;
  if (flow === undefined) {
    return undefined;
  }
  const status = view.instances.get(view.schema, view.id, { fields: ['status'] })?.data.status;
  return typeof status === 'string' ? { flow, status } : undefined;
}

// counts reports whether an expiry counts: unless the instance is in a
// terminal state of its Workflow, where its work is finished.
function counts(status: Status | undefined): boolean {
  return status === undefined || !isTerminalState(status.flow, status.status);
}

// end clears the holder and advances the token, recording how the lease
// ended, and deletes the directives of the lease it ends, so none of them
// reaches the next holder.
function end(context: OperationContext<LeaseConfig>, lease: Held, expiries: number, ended: LeaseEnd): void {
  context.columns.set({
    holder: null,
    token: lease.token + 1,
    acquired_at: null,
    renewed_at: null,
    expires_at: null,
    ttl_ms: null,
    expiries,
    ended_reason: ended,
    ended_at: context.now,
  });
  context.sql.run(`DELETE FROM ${context.sql.table('directives')} WHERE namespace = ? AND schema = ? AND id = ? AND token = ?`, [
    ...key(context),
    lease.token,
  ]);
}

// move moves the status through Workflow's transition when it is one of
// the rule's from states. A guard's veto leaves it as it is; anything else,
// a permission the caller lacks say, fails the call.
function move(context: OperationContext<LeaseConfig>, rule: LeaseTransition | undefined, status: Status | undefined): void {
  if (rule === undefined || status === undefined || !rule.from.includes(status.status)) {
    return;
  }
  try {
    context.call('Workflow', 'transition', { to: rule.transition });
  } catch (error) {
    if (!(error instanceof BehaviorVetoError)) {
      throw error;
    }
  }
}

// applyExpiry ends a lease as an expiry: the one code expire, acquire and
// an abandon share.
function applyExpiry(context: OperationContext<LeaseConfig>, lease: Held, ended: LeaseEnd): void {
  const { config } = context;
  const status = statusOf(context);
  const counted = counts(status);
  const expiries = lease.expiries + (counted ? 1 : 0);
  end(context, lease, expiries, ended);
  const capped = counted && config.maxExpiries !== undefined && expiries >= config.maxExpiries;
  move(context, capped && config.escalate !== undefined ? config.escalate : config.onExpiry, status);
}

// holding holds heartbeat and acknowledge to the holder of an active
// lease, presenting its token; the guard has refused a stale one.
function holding(view: InstanceView<LeaseConfig>, lease: Held, token: number | undefined): GuardAnswer {
  if (lease.holder === null) {
    return veto('it is not leased', 'not_leased');
  }
  if (lease.holder !== view.principal.subject) {
    return veto('another principal holds its lease', 'not_holder');
  }
  if (token === undefined) {
    return veto("the holder presents its token as Lease's precondition", 'token_required');
  }
  if (!isActive(view, lease)) {
    return lapsed(view, lease, (at) => `its lease expired at ${at}; expire applies the expiry, and acquire takes a new lease`);
  }
  return undefined;
}

// releasing holds release to the holder of an active lease, presenting its
// token, or a principal with overridePermission, who need not.
function releasing(view: InstanceView<LeaseConfig>, lease: Held, token: number | undefined): GuardAnswer {
  if (lease.holder === null) {
    return veto('it is not leased', 'not_leased');
  }
  const override = overrides(view);
  if (lease.holder !== view.principal.subject && !override) {
    if (view.config.overridePermission !== undefined) {
      throw forbidden(view, "release another principal's lease of", view.config.overridePermission);
    }
    return veto('another principal holds its lease, and only the holder may release it', 'not_holder');
  }
  if (token === undefined && !override) {
    return veto("the holder releases its lease presenting its token as Lease's precondition", 'token_required');
  }
  if (!isActive(view, lease)) {
    return lapsed(view, lease, (at) => `its lease expired at ${at}; expire applies the expiry`);
  }
  return undefined;
}

// ownOperation is the guard of Lease's own operations: who may send a
// directive, and the holder and the token heartbeat, acknowledge and
// release need. A behavior's call() of direct was asked already.
function ownOperation(view: InstanceView<LeaseConfig>, request: GuardRequest & { kind: 'operation' }, lease: Held, token: number | undefined): GuardAnswer {
  switch (request.operation) {
    case 'direct':
      return request.caller === undefined ? directGuard(view) : undefined;
    case 'heartbeat':
    case 'acknowledge':
      return holding(view, lease, token);
    case 'release':
      return releasing(view, lease, token);
    default:
      return undefined;
  }
}

// checkGuarded is a handler's own check that the guard let only the holder
// of an active lease through: anything else is a defect.
function checkGuarded(context: OperationContext<LeaseConfig>, operation: string, lease: Held): void {
  if (lease.holder !== context.principal.subject || !isActive(context, lease)) {
    throw new BehaviorError(NAME, `${operation} reached its handler without its guard`);
  }
}

function directiveOf(row: Row): DirectiveRecord {
  return {
    id: Number(row.directive),
    name: String(row.name),
    ...(row.data === null ? {} : { data: JSON.parse(String(row.data)) as Record<string, unknown> }),
    ...(row.dedupe_key === null ? {} : { dedupeKey: String(row.dedupe_key) }),
    createdAt: Number(row.created_at),
    createdBy: String(row.created_by),
  };
}

function states(flow: Flow): string {
  return flow.states.join(', ');
}

/** A Workflow config as the schema holds it: its states and transitions. */
interface Flow {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

// checkMove holds a status move of the config to the type's Workflow:
// every state one of its states, and a transition from each from state
// to the target.
function checkMove(flow: Flow, at: string, rule: LeaseTransition): void {
  if (!flow.states.includes(rule.transition)) {
    throw new BehaviorConfigError(`${at}.transition "${rule.transition}" is not a state of the type's Workflow (${states(flow)})`);
  }
  for (const from of rule.from) {
    if (!flow.states.includes(from)) {
      throw new BehaviorConfigError(`${at}.from names "${from}", which is not a state of the type's Workflow (${states(flow)})`);
    }
    if (from === rule.transition) {
      throw new BehaviorConfigError(`${at} moves the status from "${from}" to itself`);
    }
    if (!flow.transitions.some((transition) => transition.from === from && transition.to === rule.transition)) {
      throw new BehaviorConfigError(`${at}: no transition of the type's Workflow leads from "${from}" to "${rule.transition}"`);
    }
  }
}

// checkIntegerField holds maxHoldField to an integer field of the type.
function checkIntegerField(target: ConfigTarget, field: string): void {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`maxHoldField "${field}" is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  const type = (target.fieldSchemas[field] as { type?: unknown } | undefined)?.type;
  const types = Array.isArray(type) ? type : [type];
  if (!types.includes('integer') || types.some((one) => one !== 'integer' && one !== 'null')) {
    throw new BehaviorConfigError(`maxHoldField "${field}" is not an integer field of ${target.type}`);
  }
}

// requireOverride holds a call to a principal with overridePermission,
// and refuses it outright when the config names none.
function requireOverride(scope: BehaviorScope<LeaseConfig>, what: string): void {
  const permission = scope.config.overridePermission;
  if (permission === undefined) {
    throw new EngineError('forbidden', `${scope.principal.subject} may not ${what} ${scope.schema}: its Lease config names no overridePermission`);
  }
  if (!scope.can(permission)) {
    throw new EngineError('forbidden', `${scope.principal.subject} may not ${what} ${scope.schema}: it needs permission ${permission}`);
  }
}

// directGuard is who may send a directive: a principal with
// directPermission, or overridePermission when that is absent. A
// behavior's call() is not asked (the guard passes it).
function directGuard(view: InstanceView<LeaseConfig>): GuardAnswer {
  const permission = view.config.directPermission ?? view.config.overridePermission;
  if (permission === undefined) {
    return veto('its config names no permission that sends directives (directPermission or overridePermission)', 'not_configured');
  }
  if (!view.can(permission)) {
    throw forbidden(view, 'send a directive to the holder of', permission);
  }
  return undefined;
}

// holdSql is the longest hold as SQL over the relation's row: the
// instance's maxHoldField when it holds a positive integer, else
// maxHoldMs; no SQL when the config gives neither.
function holdSql(config: LeaseConfig): { sql?: string; params: SqlValue[] } {
  if (config.maxHoldField === undefined) {
    return config.maxHoldMs === undefined ? { params: [] } : { sql: '?', params: [config.maxHoldMs] };
  }
  const path = `$."${config.maxHoldField}"`;
  return {
    sql: `COALESCE(CASE WHEN json_type(data, ?) = 'integer' AND json_extract(data, ?) > 0 THEN json_extract(data, ?) END, ?)`,
    params: [path, path, path, config.maxHoldMs ?? null],
  };
}

// expireOne invokes expire on one instance for a sweep or expireHolder,
// and returns why it expired, or undefined. A guard's veto leaves that
// instance for the next sweep and the others go on; any other failure, a
// principal the policy refuses say, fails the run, so the deployment
// sees it.
function expireOne(context: BehaviorScope<LeaseConfig>, id: string, params: { holder?: string; notRenewedAfter?: number }): ExpiryReason | undefined {
  try {
    return (context.instances.invoke(context.schema, id, 'expire', params as FrozenJSON) as { reason?: ExpiryReason }).reason;
  } catch (error) {
    if (error instanceof BehaviorVetoError) {
      return undefined;
    }
    throw error;
  }
}

// acknowledgeDirectives marks directives of the lease handled, for
// acknowledge and heartbeat's acknowledge; an id not sent under its token
// refuses the call.
function acknowledgeDirectives(context: OperationContext<LeaseConfig>, operation: string, path: string, lease: Held, ids: readonly number[]): void {
  const table = context.sql.table('directives');
  const marks = ids.map(() => '?').join(', ');
  const found = new Set(
    context.sql
      .all(`SELECT directive FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND directive IN (${marks})`, [
        ...key(context),
        lease.token,
        ...ids,
      ])
      .map((row) => Number(row.directive))
  );
  const missing = ids.filter((id) => !found.has(id));
  if (missing.length > 0) {
    throw new OperationParamsError(NAME, operation, [{ path, message: `no directive ${missing.join(', ')} was sent under token ${lease.token}` }]);
  }
  context.sql.run(
    `UPDATE ${table} SET acknowledged_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND directive IN (${marks}) AND acknowledged_at IS NULL`,
    [context.now, ...key(context), lease.token, ...ids]
  );
}

export const lease = defineBehavior<LeaseConfig>({
  declaration,

  // The configSchema holds the shape; this holds the config to the type:
  // its fields, its Workflow's states and the behaviors it lists.
  parseConfig(json, target) {
    const raw = json as {
      ttlMs?: number;
      heartbeatMs?: number;
      sweepMs?: number;
      maxHoldMs?: number;
      maxHoldField?: string;
      onExpiry?: LeaseTransition;
      maxExpiries?: number;
      escalate?: LeaseTransition;
      exempt?: string[];
      requireToken?: boolean;
      acquirePermission?: string;
      overridePermission?: string;
      directPermission?: string;
    };
    const ttlMs = raw.ttlMs ?? DEFAULT_TTL_MS;
    const heartbeatMs = raw.heartbeatMs ?? Math.floor(ttlMs / 3);
    if (heartbeatMs * 2 > ttlMs) {
      throw new BehaviorConfigError(`heartbeatMs (${heartbeatMs}) is more than half of ttlMs (${ttlMs}): one late heartbeat would lose the lease`);
    }
    if (raw.maxHoldField !== undefined) {
      checkIntegerField(target, raw.maxHoldField);
    }
    const workflow = target.configs.Workflow as Partial<Flow> | undefined;
    for (const [at, rule] of [
      ['onExpiry', raw.onExpiry],
      ['escalate', raw.escalate],
    ] as const) {
      if (rule === undefined) {
        continue;
      }
      if (!target.behaviors.includes('Workflow')) {
        throw new BehaviorConfigError(`${at} moves the status through Workflow, which the type does not list`);
      }
      // A Workflow config its own checks refuse is reported there.
      if (Array.isArray(workflow?.states) && Array.isArray(workflow.transitions)) {
        checkMove(workflow as Flow, at, rule);
      }
    }
    if (raw.escalate !== undefined && raw.maxExpiries === undefined) {
      throw new BehaviorConfigError('escalate applies at the expiry that reaches maxExpiries, which the config does not give');
    }
    for (const entry of raw.exempt ?? []) {
      const behavior = entry.slice(0, entry.lastIndexOf('.'));
      if (behavior === NAME) {
        throw new BehaviorConfigError(`exempt names ${entry}: Lease's own operations check their callers themselves`);
      }
      if (!target.behaviors.includes(behavior)) {
        throw new BehaviorConfigError(`exempt names ${entry}, but the type does not list ${behavior} (it lists ${target.behaviors.join(', ')})`);
      }
    }
    return {
      ttlMs,
      heartbeatMs,
      sweepMs: raw.sweepMs ?? DEFAULT_SWEEP_MS,
      ...(raw.maxHoldMs === undefined ? {} : { maxHoldMs: raw.maxHoldMs }),
      ...(raw.maxHoldField === undefined ? {} : { maxHoldField: raw.maxHoldField }),
      ...(raw.onExpiry === undefined ? {} : { onExpiry: { transition: raw.onExpiry.transition, from: [...raw.onExpiry.from] } }),
      ...(raw.maxExpiries === undefined ? {} : { maxExpiries: raw.maxExpiries }),
      ...(raw.escalate === undefined ? {} : { escalate: { transition: raw.escalate.transition, from: [...raw.escalate.from] } }),
      exempt: [...(raw.exempt ?? [])],
      requireToken: raw.requireToken === true,
      ...(raw.acquirePermission === undefined ? {} : { acquirePermission: raw.acquirePermission }),
      ...(raw.overridePermission === undefined ? {} : { overridePermission: raw.overridePermission }),
      ...(raw.directPermission === undefined ? {} : { directPermission: raw.directPermission }),
    };
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the leases and directives its instances hold would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'lease',
      columns: {
        holder: { type: 'text' },
        token: { type: 'integer', notNull: true, default: 0 },
        acquired_at: { type: 'integer' },
        expires_at: { type: 'integer' },
        ttl_ms: { type: 'integer' },
        expiries: { type: 'integer', notNull: true, default: 0 },
      },
      // The sweep reads the held leases, and expireHolder one holder's.
      indexes: { held: ['holder', 'expires_at'] },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('directives')} (
          namespace       TEXT    NOT NULL,
          schema          TEXT    NOT NULL,
          id              TEXT    NOT NULL,
          token           INTEGER NOT NULL,
          directive       INTEGER NOT NULL,
          name            TEXT    NOT NULL,
          data            TEXT,
          created_by      TEXT    NOT NULL,
          created_at      INTEGER NOT NULL,
          acknowledged_at INTEGER,
          PRIMARY KEY (namespace, schema, id, token, directive)
        ) STRICT`);
      },
    },
    // How the last lease ended, which its event carries.
    { version: 2, name: 'ended', columns: { ended_reason: { type: 'text' }, ended_at: { type: 'integer' } } },
    // When the holder last renewed it, which notRenewedAfter reads, and the
    // key that sends a directive once per lease.
    {
      version: 3,
      name: 'renewed',
      columns: { renewed_at: { type: 'integer' } },
      up(sql) {
        sql.run(`ALTER TABLE ${sql.table('directives')} ADD COLUMN dedupe_key TEXT`);
      },
    },
  ],

  // The lease's exclusion and its fence: see the header. A new instance
  // holds no lease, so a create has nothing to exclude or fence.
  guard(view, request) {
    if (request.kind === 'create') {
      return undefined;
    }
    const lease = held(view);
    const writes = request.kind !== 'operation' || request.writes;
    const token = presented(request);
    if (writes && token !== undefined && token !== lease.token) {
      return stale(token, lease);
    }
    if (request.kind === 'operation' && request.behavior === NAME) {
      return ownOperation(view, request, lease, token);
    }
    if (lease.holder === null || !writes || (request.kind === 'operation' && request.behavior === 'Queue' && request.operation === 'refresh')) {
      return undefined;
    }
    if (overrides(view)) {
      return undefined;
    }
    const field = view.config.maxHoldField;
    if (request.kind === 'update' && field !== undefined && Object.prototype.hasOwnProperty.call(request.patch, field)) {
      return veto(`${field} limits how long its lease may be held, so it cannot change while the lease is held`, 'hold_limit_fixed');
    }
    if (request.kind !== 'delete' && request.caller !== undefined) {
      return undefined;
    }
    if (request.kind === 'operation' && view.config.exempt.includes(`${request.behavior}.${request.operation}`)) {
      return undefined;
    }
    const mine = lease.holder === view.principal.subject;
    if (isActive(view, lease)) {
      if (!mine) {
        const until = deadline(view, lease);
        return veto(`another principal holds its lease, until ${time(until)}`, 'held_by_another', { expiresAt: until });
      }
      return view.config.requireToken && token === undefined
        ? veto("its config requires every write under the lease to present the lease's token as Lease's precondition", 'token_required')
        : undefined;
    }
    return mine ? lapsed(view, lease, (at) => `the caller's lease expired at ${at}, so it no longer holds the instance`) : undefined;
  },

  operations: {
    acquire(context, params) {
      const { config } = context;
      if (config.acquirePermission !== undefined && !context.can(config.acquirePermission)) {
        throw forbidden(context, 'acquire the lease of', config.acquirePermission);
      }
      const ttl = (params.ttlMs as number | undefined) ?? config.ttlMs;
      if (ttl > config.ttlMs) {
        throw new OperationParamsError(NAME, 'acquire', [{ path: '/ttlMs', message: `${ttl} is longer than the config's ttlMs, ${config.ttlMs}` }]);
      }
      let lease = held(context);
      if (isActive(context, lease)) {
        const until = deadline(context, lease);
        throw lease.holder === context.principal.subject
          ? vetoed(context, 'acquire', `the caller already holds its lease, until ${time(until)}; heartbeat renews it`, 'held_by_caller', { expiresAt: until })
          : vetoed(context, 'acquire', `another principal holds its lease, until ${time(until)}`, 'held_by_another', { expiresAt: until });
      }
      const lapsed = lease.holder !== null;
      if (config.maxExpiries !== undefined) {
        const expiries = lease.expiries + (lapsed && counts(statusOf(context)) ? 1 : 0);
        if (expiries >= config.maxExpiries) {
          throw vetoed(
            context,
            'acquire',
            `its lease has expired ${expiries} times, the most its config allows; resetExpiries lets it be leased again`,
            'max_expiries',
            { expiries, maxExpiries: config.maxExpiries }
          );
        }
      }
      if (lapsed) {
        applyExpiry(context, lease, lapseReason(context, lease));
        lease = held(context);
      }
      const token = lease.token + 1;
      const limit = holdLimit(context);
      const expiresAt = limit === undefined ? context.now + ttl : context.now + Math.min(ttl, limit);
      context.columns.set({
        holder: context.principal.subject,
        token,
        acquired_at: context.now,
        renewed_at: context.now,
        expires_at: expiresAt,
        ttl_ms: ttl,
        ended_reason: null,
        ended_at: null,
      });
      return { token, expiresAt, heartbeatMs: heartbeatFor(config, ttl) };
    },

    // The guard holds it to the holder of an active lease, with its token.
    // Its acknowledge marks directives handled in the same write.
    heartbeat(context, params) {
      const lease = held(context);
      checkGuarded(context, 'heartbeat', lease);
      const ids = params.acknowledge as number[] | undefined;
      if (ids !== undefined && ids.length > 0) {
        acknowledgeDirectives(context, 'heartbeat', '/acknowledge', lease, ids);
      }
      const ttl = lease.ttlMs ?? context.config.ttlMs;
      const limit = holdLimit(context);
      const expiresAt = limit === undefined ? context.now + ttl : Math.min(context.now + ttl, (lease.acquiredAt as number) + limit);
      context.columns.set({ expires_at: expiresAt, renewed_at: context.now });
      const directives = context.sql
        .all(
          `SELECT directive, name, data, dedupe_key, created_by, created_at FROM ${context.sql.table('directives')}
           WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND acknowledged_at IS NULL ORDER BY directive`,
          [...key(context), lease.token]
        )
        .map(directiveOf);
      return { expiresAt, directives };
    },

    // The guard holds it to the holder of an active lease, with its token,
    // or a principal with overridePermission. An abandon is an expiry.
    release(context, params) {
      const { config } = context;
      const lease = held(context);
      if (lease.holder === null || !isActive(context, lease)) {
        throw new BehaviorError(NAME, 'release reached its handler without its guard');
      }
      if (params.abandon === true) {
        applyExpiry(context, lease, 'abandon');
        return {};
      }
      const status = config.onExpiry === undefined ? undefined : statusOf(context);
      end(context, lease, lease.expiries, 'release');
      move(context, config.onExpiry, status);
      return {};
    },

    expire(context, params) {
      const holder = params.holder as string | undefined;
      const notRenewedAfter = params.notRenewedAfter as number | undefined;
      if (holder === undefined && notRenewedAfter !== undefined) {
        throw new OperationParamsError(NAME, 'expire', [
          { path: '/notRenewedAfter', message: "notRenewedAfter spares a holder's active leases, so it needs holder" },
        ]);
      }
      if (holder !== undefined) {
        requireOverride(context, 'expire the lease of another holder of');
      }
      const lease = held(context);
      if (lease.holder === null || (holder === undefined ? isActive(context, lease) : lease.holder !== holder)) {
        return { expired: false };
      }
      const active = isActive(context, lease);
      // Its holder renewed it after the time: the process holding it is alive.
      if (active && notRenewedAfter !== undefined && (lease.renewedAt ?? lease.acquiredAt ?? 0) > notRenewedAfter) {
        return { expired: false };
      }
      const reason: ExpiryReason = active ? 'holder' : lapseReason(context, lease);
      applyExpiry(context, lease, reason);
      return { expired: true, reason };
    },

    // Who may send one is the guard's question (directGuard).
    direct(context, params) {
      const lease = held(context);
      if (lease.holder === null) {
        throw vetoed(context, 'direct', 'no lease is active, so there is no holder to direct', 'not_leased');
      }
      if (!isActive(context, lease)) {
        throw vetoed(context, 'direct', 'no lease is active, so there is no holder to direct', 'lapsed', { expiredAt: deadline(context, lease) });
      }
      const table = context.sql.table('directives');
      const dedupeKey = params.dedupeKey as string | undefined;
      if (dedupeKey !== undefined) {
        const sent = context.sql.get(`SELECT directive FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND dedupe_key = ?`, [
          ...key(context),
          lease.token,
          dedupeKey,
        ]);
        if (sent !== undefined) {
          return { id: Number(sent.directive), created: false };
        }
      }
      const last = context.sql.get(`SELECT MAX(directive) AS last FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ?`, [
        ...key(context),
        lease.token,
      ]);
      const id = Number(last?.last ?? 0) + 1;
      const data = params.data as FrozenJSON | undefined;
      context.sql.run(
        `INSERT INTO ${table} (namespace, schema, id, token, directive, name, data, dedupe_key, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          ...key(context),
          lease.token,
          id,
          params.name as string,
          data === undefined ? null : JSON.stringify(data),
          dedupeKey ?? null,
          context.principal.subject,
          context.now,
        ]
      );
      return { id, created: true };
    },

    // The guard holds it to the holder of an active lease, with its token.
    acknowledge(context, params) {
      const lease = held(context);
      checkGuarded(context, 'acknowledge', lease);
      acknowledgeDirectives(context, 'acknowledge', '/ids', lease, params.ids as number[]);
      return {};
    },

    resetExpiries(context) {
      const permission = context.config.overridePermission;
      if (permission === undefined) {
        throw vetoed(context, 'resetExpiries', 'its config names no overridePermission, which resetting its expiries needs', 'not_configured');
      }
      if (!context.can(permission)) {
        throw forbidden(context, 'reset the expiries of', permission);
      }
      const { expiries } = held(context);
      context.columns.set({ expiries: 0 });
      return { expiries };
    },
  },

  schemaOperations: {
    // Every lease one holder has on the schema, found through the index on
    // the holder, expired by expire on each instance, so each expiry runs
    // that instance's guards and appends its event.
    expireHolder(context, params) {
      requireOverride(context, 'expire the leases of a holder on');
      const holder = params.holder as string;
      const notRenewedAfter = params.notRenewedAfter as number | undefined;
      const relation = context.sql.instances();
      const expired: string[] = [];
      const reasons: Partial<Record<ExpiryReason, number>> = {};
      let after = '';
      for (;;) {
        const ids = context.sql
          .all(`SELECT id FROM ${relation} WHERE holder = ? AND id > ? ORDER BY id LIMIT ?`, [holder, after, BATCH])
          .map((row) => String(row.id));
        for (const id of ids) {
          const reason = expireOne(context, id, notRenewedAfter === undefined ? { holder } : { holder, notRenewedAfter });
          if (reason !== undefined) {
            expired.push(id);
            reasons[reason] = (reasons[reason] ?? 0) + 1;
          }
        }
        if (ids.length < BATCH) {
          return { expired: expired.length, reasons, ids: expired };
        }
        after = ids[ids.length - 1];
      }
    },
  },

  schedules: {
    // Every sweepMs, the runner expires the leases past their expiry time
    // or their longest hold, at most MAX_SWEEP a run.
    expire: {
      everyMs: (config) => config.sweepMs,
      run(context) {
        const { config } = context;
        const relation = context.sql.instances();
        const hold = holdSql(config);
        let after = '';
        for (let swept = 0; swept < MAX_SWEEP; ) {
          const ids = context.sql
            .all(
              `SELECT id FROM ${relation}
               WHERE holder IS NOT NULL AND id > ? AND (expires_at <= ?${hold.sql === undefined ? '' : ` OR acquired_at + ${hold.sql} <= ?`})
               ORDER BY id LIMIT ?`,
              [after, context.now, ...hold.params, ...(hold.sql === undefined ? [] : [context.now]), Math.min(BATCH, MAX_SWEEP - swept)]
            )
            .map((row) => String(row.id));
          for (const id of ids) {
            expireOne(context, id, {});
          }
          swept += ids.length;
          if (ids.length < BATCH) {
            return;
          }
          after = ids[ids.length - 1];
        }
      },
    },
  },

  fields: {
    lease(view): LeaseRecord {
      const lease = held(view);
      return {
        holder: lease.holder,
        token: lease.token,
        acquiredAt: lease.acquiredAt,
        renewedAt: lease.holder === null ? null : (lease.renewedAt ?? lease.acquiredAt),
        expiresAt: lease.holder === null ? null : deadline(view, lease),
        active: isActive(view, lease),
        expiries: lease.expiries,
        ended: lease.endedReason === null ? null : { reason: lease.endedReason, at: lease.endedAt as number },
      };
    },
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('directives')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});
