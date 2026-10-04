/*
Lease, an exclusive, time-bounded lease on an instance (D16), held by one
principal and numbered by a fencing token. The token is a per-instance
integer that starts at 0 and advances at every acquire, every release and
every applied expiry, so (instance, token) names one lease: a heartbeat,
a release or an acknowledgement under any other token is refused, and so
is one from a principal that does not hold the lease. The token is no
capability: the guard below checks who calls, and the token tells two
leases of one principal apart, a worker's process that lost its lease
from the one that took a new one.

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
holder has on the schema (expireHolder), for a holder that is gone. An expiry clears the holder, advances the token, counts the expiry
unless the instance is in a terminal state of its Workflow (a holder that
finished and died before releasing has not failed), and moves the status
with onExpiry, or escalate at the expiry that reaches maxExpiries, only
from the config's from states and only through Workflow's transition,
whose guards run. A transition a guard vetoes leaves the status as it is
and the lease expired. Once the instance has had maxExpiries expiries, it
cannot be leased until a principal with overridePermission resets them.
A release applies onExpiry too, without counting an expiry, so a holder
that gives up leaves the work where it can be taken again.

The guard keeps a lease exclusive. While a lease is active it refuses an
update, a delete and every writing operation of another behavior by any
principal but the holder, except an operation the config exempts and a
principal with overridePermission. A read-only operation passes, and so
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
current token; heartbeat returns the ones not acknowledged, and
acknowledge marks them handled. A principal's direct needs
directPermission (overridePermission when that is absent), which the
guard asks; another behavior of the type sends one through call(), as
the principal it runs for, with none, as a budget does when its usage
runs over. When the token advances, the directives
of the lease it ends are deleted, so none reaches the next holder.

configChange: any config may change. Lease can be added to a schema that
has instances, which start free at token 0, and cannot be removed from
one: the leases and directives its instances hold would stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  isTerminalState,
  type BehaviorScope,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceView,
  type OperationContext,
  type Row,
  type SqlValue,
  type WorkflowStates,
} from '@superschematic/engine';

import declaration from './declarations/Lease.behavior.json' with { type: 'json' };

/** How long a lease lasts after its acquire or its last heartbeat when the config gives no ttlMs. */
export const DEFAULT_TTL_MS = 60000;

/** How often the runner expires lapsed leases when the config gives no sweepMs. */
export const DEFAULT_SWEEP_MS = 5000;

/** The instances one query of the sweep or of expireHolder reads. */
const BATCH = 100;

/** The most leases one run of the expire schedule expires; the rest wait for the next. */
export const MAX_SWEEP = 1000;

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
  /** When the lease stops being active: its expiry time, or its longest hold if that comes first; null when free. */
  readonly expiresAt: number | null;
  /** Whether it is held and neither expired nor past its longest hold. */
  readonly active: boolean;
  /** How many expiries the instance has had. */
  readonly expiries: number;
}

/** A directive, as heartbeat returns it. */
export interface DirectiveRecord {
  /** Its number in its lease, 1, 2, 3, .... */
  readonly id: number;
  readonly name: string;
  readonly data?: Record<string, unknown>;
  readonly createdAt: number;
  readonly createdBy: string;
}

const NAME = 'Lease';

/** The lease's own columns on an instance. */
interface Held {
  readonly holder: string | null;
  readonly token: number;
  readonly acquiredAt: number | null;
  readonly expiresAt: number | null;
  /** The length the lease was acquired with, which each heartbeat renews it by. */
  readonly ttlMs: number | null;
  readonly expiries: number;
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
    expiresAt: number(columns.expires_at),
    ttlMs: number(columns.ttl_ms),
    expiries: Number(columns.expiries),
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

function time(ms: number): string {
  return new Date(ms).toISOString();
}

function vetoed(view: InstanceView<unknown>, operation: string, reason: string): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, reason);
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

// end clears the holder and advances the token, deleting the directives
// of the lease it ends, so none of them reaches the next holder.
function end(context: OperationContext<LeaseConfig>, lease: Held, expiries: number): void {
  context.columns.set({ holder: null, token: lease.token + 1, acquired_at: null, expires_at: null, ttl_ms: null, expiries });
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

// applyExpiry ends a lapsed lease as an expiry: the one code expire and
// acquire share.
function applyExpiry(context: OperationContext<LeaseConfig>, lease: Held): void {
  const { config } = context;
  const status = statusOf(context);
  const counted = counts(status);
  const expiries = lease.expiries + (counted ? 1 : 0);
  end(context, lease, expiries);
  const capped = counted && config.maxExpiries !== undefined && expiries >= config.maxExpiries;
  move(context, capped && config.escalate !== undefined ? config.escalate : config.onExpiry, status);
}

// checkHolder holds heartbeat and acknowledge to the holder of an active
// lease, with its current token.
function checkHolder(context: OperationContext<LeaseConfig>, operation: string, lease: Held, token: number): void {
  if (lease.holder === null) {
    throw vetoed(context, operation, 'it is not leased');
  }
  if (lease.holder !== context.principal.subject) {
    throw vetoed(context, operation, 'another principal holds its lease');
  }
  if (token !== lease.token) {
    throw vetoed(context, operation, `token ${token} is stale: the lease is at token ${lease.token}`);
  }
  if (!isActive(context, lease)) {
    throw vetoed(context, operation, `its lease expired at ${time(deadline(context, lease))}; expire applies the expiry, and acquire takes a new lease`);
  }
}

function directiveOf(row: Row): DirectiveRecord {
  return {
    id: Number(row.directive),
    name: String(row.name),
    ...(row.data === null ? {} : { data: JSON.parse(String(row.data)) as Record<string, unknown> }),
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
function directGuard(view: InstanceView<LeaseConfig>): string | undefined {
  const permission = view.config.directPermission ?? view.config.overridePermission;
  if (permission === undefined) {
    return 'its config names no permission that sends directives (directPermission or overridePermission)';
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

// expireOne invokes expire on one instance for a sweep or expireHolder. A
// guard's veto leaves that instance for the next sweep and the others go
// on; any other failure, a principal the policy refuses say, fails the
// run, so the deployment sees it.
function expireOne(context: BehaviorScope<LeaseConfig>, id: string, params: { holder?: string }): boolean {
  try {
    return (context.instances.invoke(context.schema, id, 'expire', params as FrozenJSON) as { expired: boolean }).expired;
  } catch (error) {
    if (error instanceof BehaviorVetoError) {
      return false;
    }
    throw error;
  }
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
      acquirePermission?: string;
      overridePermission?: string;
      directPermission?: string;
    };
    const ttlMs = raw.ttlMs ?? DEFAULT_TTL_MS;
    const heartbeatMs = raw.heartbeatMs ?? Math.floor(ttlMs / 3);
    if (heartbeatMs >= ttlMs) {
      throw new BehaviorConfigError(`heartbeatMs (${heartbeatMs}) is not less than ttlMs (${ttlMs}): a lease would expire between heartbeats`);
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
  ],

  // The lease's exclusion: see the header. A new instance holds no lease.
  guard(view, request) {
    if (request.kind === 'create') {
      return undefined;
    }
    if (request.kind === 'operation' && request.behavior === NAME) {
      return request.operation === 'direct' && request.caller === undefined ? directGuard(view) : undefined;
    }
    const lease = held(view);
    if (lease.holder === null) {
      return undefined;
    }
    if (request.kind === 'operation' && (!request.writes || (request.behavior === 'Queue' && request.operation === 'refresh'))) {
      return undefined;
    }
    if (overrides(view)) {
      return undefined;
    }
    const field = view.config.maxHoldField;
    if (request.kind === 'update' && field !== undefined && Object.prototype.hasOwnProperty.call(request.patch, field)) {
      return `${field} limits how long its lease may be held, so it cannot change while the lease is held`;
    }
    if (request.kind !== 'delete' && request.caller !== undefined) {
      return undefined;
    }
    if (request.kind === 'operation' && view.config.exempt.includes(`${request.behavior}.${request.operation}`)) {
      return undefined;
    }
    const mine = lease.holder === view.principal.subject;
    if (isActive(view, lease)) {
      return mine ? undefined : `another principal holds its lease, until ${time(deadline(view, lease))}`;
    }
    return mine ? `the caller's lease expired at ${time(deadline(view, lease))}, so it no longer holds the instance` : undefined;
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
        throw vetoed(
          context,
          'acquire',
          lease.holder === context.principal.subject
            ? `the caller already holds its lease, until ${time(deadline(context, lease))}; heartbeat renews it`
            : `another principal holds its lease, until ${time(deadline(context, lease))}`
        );
      }
      const lapsed = lease.holder !== null;
      if (config.maxExpiries !== undefined) {
        const expiries = lease.expiries + (lapsed && counts(statusOf(context)) ? 1 : 0);
        if (expiries >= config.maxExpiries) {
          throw vetoed(context, 'acquire', `its lease has expired ${expiries} times, the most its config allows; resetExpiries lets it be leased again`);
        }
      }
      if (lapsed) {
        applyExpiry(context, lease);
        lease = held(context);
      }
      const token = lease.token + 1;
      const limit = holdLimit(context);
      const expiresAt = limit === undefined ? context.now + ttl : context.now + Math.min(ttl, limit);
      context.columns.set({ holder: context.principal.subject, token, acquired_at: context.now, expires_at: expiresAt, ttl_ms: ttl });
      return { token, expiresAt, heartbeatMs: heartbeatFor(config, ttl) };
    },

    heartbeat(context, params) {
      const lease = held(context);
      checkHolder(context, 'heartbeat', lease, params.token as number);
      const ttl = lease.ttlMs ?? context.config.ttlMs;
      const limit = holdLimit(context);
      const expiresAt = limit === undefined ? context.now + ttl : Math.min(context.now + ttl, (lease.acquiredAt as number) + limit);
      context.columns.set({ expires_at: expiresAt });
      const directives = context.sql
        .all(
          `SELECT directive, name, data, created_by, created_at FROM ${context.sql.table('directives')}
           WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND acknowledged_at IS NULL ORDER BY directive`,
          [...key(context), lease.token]
        )
        .map(directiveOf);
      return { expiresAt, directives };
    },

    release(context, params) {
      const { config } = context;
      const lease = held(context);
      if (lease.holder === null) {
        throw vetoed(context, 'release', 'it is not leased');
      }
      const override = overrides(context);
      const token = params.token as number | undefined;
      if (lease.holder !== context.principal.subject && !override) {
        if (config.overridePermission !== undefined) {
          throw forbidden(context, "release another principal's lease of", config.overridePermission);
        }
        throw vetoed(context, 'release', 'another principal holds its lease, and only the holder may release it');
      }
      if (token === undefined && !override) {
        throw new OperationParamsError(NAME, 'release', [{ path: '/token', message: 'the holder releases its lease with its token' }]);
      }
      if (token !== undefined && token !== lease.token) {
        throw vetoed(context, 'release', `token ${token} is stale: the lease is at token ${lease.token}`);
      }
      if (!isActive(context, lease)) {
        throw vetoed(context, 'release', `its lease expired at ${time(deadline(context, lease))}; expire applies the expiry`);
      }
      const status = config.onExpiry === undefined ? undefined : statusOf(context);
      end(context, lease, lease.expiries);
      move(context, config.onExpiry, status);
      return {};
    },

    expire(context, params) {
      const holder = params.holder as string | undefined;
      if (holder !== undefined) {
        requireOverride(context, 'expire the lease of another holder of');
      }
      const lease = held(context);
      if (lease.holder === null || (holder === undefined ? isActive(context, lease) : lease.holder !== holder)) {
        return { expired: false };
      }
      applyExpiry(context, lease);
      return { expired: true };
    },

    // Who may send one is the guard's question (directGuard).
    direct(context, params) {
      const lease = held(context);
      if (!isActive(context, lease)) {
        throw vetoed(context, 'direct', 'no lease is active, so there is no holder to direct');
      }
      const table = context.sql.table('directives');
      const last = context.sql.get(`SELECT MAX(directive) AS last FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND token = ?`, [
        ...key(context),
        lease.token,
      ]);
      const id = Number(last?.last ?? 0) + 1;
      const data = params.data as FrozenJSON | undefined;
      context.sql.run(
        `INSERT INTO ${table} (namespace, schema, id, token, directive, name, data, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [...key(context), lease.token, id, params.name as string, data === undefined ? null : JSON.stringify(data), context.principal.subject, context.now]
      );
      return { id };
    },

    acknowledge(context, params) {
      const lease = held(context);
      checkHolder(context, 'acknowledge', lease, params.token as number);
      const ids = params.ids as number[];
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
        throw new OperationParamsError(NAME, 'acknowledge', [
          { path: '/ids', message: `no directive ${missing.join(', ')} was sent under token ${lease.token}` },
        ]);
      }
      context.sql.run(
        `UPDATE ${table} SET acknowledged_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND token = ? AND directive IN (${marks}) AND acknowledged_at IS NULL`,
        [context.now, ...key(context), lease.token, ...ids]
      );
      return {};
    },

    resetExpiries(context) {
      const permission = context.config.overridePermission;
      if (permission === undefined) {
        throw vetoed(context, 'resetExpiries', 'its config names no overridePermission, which resetting its expiries needs');
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
      const relation = context.sql.instances();
      let expired = 0;
      let after = '';
      for (;;) {
        const ids = context.sql
          .all(`SELECT id FROM ${relation} WHERE holder = ? AND id > ? ORDER BY id LIMIT ?`, [holder, after, BATCH])
          .map((row) => String(row.id));
        for (const id of ids) {
          if (expireOne(context, id, { holder })) {
            expired += 1;
          }
        }
        if (ids.length < BATCH) {
          return { expired };
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
        expiresAt: lease.holder === null ? null : deadline(view, lease),
        active: isActive(view, lease),
        expiries: lease.expiries,
      };
    },
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('directives')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});
