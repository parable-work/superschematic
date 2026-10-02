/*
Queue, claimable work (D16; D16, amended: behaviors that serve claimable
work). claim takes an instance for its caller in one transaction: it
checks that the instance can be claimed, takes its lease through Lease's
acquire, reserves its budget through Budget's reserve when the type
composes Budget, and moves its status to the claimed state through
Workflow's transition. A refusal at any step leaves none of them done.
claimNext, schema-level, claims the first instance the caller can claim.

claim's checks are the real ones, read from the instance as it is: its
status is one of the claimable states (claim.from) and, when the type
composes Dependencies, no blocker holds it up. A lapsed lease is expired
first, through Lease's expire, so a holder that died leaves the instance
in the state onExpiry moves it to before the checks read it. Lease's
acquire refuses an active lease and an instance at maxExpiries, and
Assignment's guard an assigned instance's claim by anyone but its
assignee. A direct acquire on a type that composes Queue is refused: the
lease is taken by claiming, so no one holds it without the checks.

claimNext reads candidates from Queue's own columns across the schema
(sql.instances()), through their index: copies of the facts a claim
checks, which Queue keeps current in the transaction of every change of
the instance (afterChange) and of every change of a blocker it reads
(afterReferenceChange, which invokes refresh). They are the status, the
blocked field of Dependencies, the assignee of Assignment, the expiries
of Lease and the priority field's value, not booleans derived from the
config, so a new version that changes claim.from or maxExpiries applies
to them at once. A candidate is in a claimable state, not blocked, at
fewer than maxExpiries expiries, unassigned or assigned to the caller,
and holds each match value; candidates go highest priority first, an
instance without one last, then oldest, then by id. claimNext invokes
claim on each in turn, as the caller, until one succeeds, taking a veto
or a conflict to mean that one cannot be claimed now, up to
maxCandidates. A stale copy therefore costs a candidate that is skipped,
never a wrong claim. The copy of the priority is the field's value when
the instance last changed, so a new priorityField orders an instance from
its next change.

Queue records a reference to each blocker it reads through Dependencies'
listBlockers, so a blocker's change, in any schema, runs its
afterReferenceChange, which invokes refresh on the dependent as the
principal who changed the blocker. Lease's guard lets refresh through.

configChange: claim, priorityField, match and maxCandidates may change.
Queue goes on a schema before it has instances: the ones that exist
would have no copies to be found by, so it is not added to a schema with
instances, and not removed from one, since its copies and references
would stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceContext,
  type InstanceView,
  type Reference,
  type SqlValue,
} from '@superschematic/engine';

import declaration from './declarations/Queue.behavior.json' with { type: 'json' };

/** The most instances one claimNext tries when the config gives no maxCandidates. */
export const DEFAULT_MAX_CANDIDATES = 100;

/** Queue's config, parsed: the defaults filled in, and what it reads of the type's other behaviors. */
export interface QueueConfig {
  readonly claim: { readonly from: readonly string[]; readonly to: string };
  readonly priorityField?: string;
  readonly match: readonly string[];
  readonly maxCandidates: number;
  /** Whether the type composes Dependencies, whose blocked field a claim waits on. */
  readonly dependencies: boolean;
  /** Whether the type composes Budget, whose reserve a claim calls. */
  readonly budget: boolean;
  /** The type's Lease maxExpiries: an instance with that many is no candidate. */
  readonly maxExpiries?: number;
}

/** A claim, as claim returns it and claimNext's claimed holds it. */
export interface ClaimRecord {
  readonly id: string;
  readonly token: number;
  readonly expiresAt: number;
  readonly heartbeatMs: number;
}

const NAME = 'Queue';

// Dependencies' largest page of listBlockers.
const PAGE = 500;

/** The facts of one instance claimNext filters and orders on: Queue's own columns. */
interface Facts {
  readonly status: string | null;
  readonly blocked: number;
  readonly assignee: string | null;
  readonly expiries: number;
  readonly priority: number | null;
}

/** A Workflow config as the schema holds it: its states and transitions. */
interface Flow {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

function vetoed(view: InstanceView<unknown>, operation: string, reason: string): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, reason);
}

function refKey(reference: { schema: string; id: string }): string {
  return `${reference.schema}\u0000${reference.id}`;
}

// blockersOf reads the instance's blockers through Dependencies'
// listBlockers, a page at a time, keeps a reference to each that exists,
// drops the references to the rest, and reports whether one is open.
function blockersOf(context: InstanceContext<QueueConfig>): boolean {
  const found = new Map<string, { schema: string; id: string }>();
  let open = false;
  let cursor: string | undefined;
  do {
    const page = context.call('Dependencies', 'listBlockers', { limit: PAGE, ...(cursor === undefined ? {} : { cursor }) }) as {
      items: Array<{ schema: string; id: string; status?: string; open: boolean }>;
      next: string | null;
    };
    for (const blocker of page.items) {
      open ||= blocker.open;
      // A blocker without a status is one whose delete is removing its edge.
      if (blocker.status !== undefined) {
        found.set(refKey(blocker), { schema: blocker.schema, id: blocker.id });
      }
    }
    cursor = page.next ?? undefined;
  } while (cursor !== undefined);
  const held = new Set<string>();
  for (const reference of context.references.list()) {
    if (found.has(refKey(reference))) {
      held.add(refKey(reference));
    } else {
      context.references.remove(reference.schema, reference.id, reference.key);
    }
  }
  for (const [key, blocker] of found) {
    if (!held.has(key)) {
      context.references.add(blocker.schema, blocker.id);
    }
  }
  return open;
}

// factsOf reads the facts a claim checks from the instance as it is now:
// its own priority field, and the status, lease and assignee fields of the
// behaviors that keep them, read as the caller.
function factsOf(context: InstanceContext<QueueConfig>): Facts {
  const { config } = context;
  const fields = context.instances.get(context.schema, context.id, { fields: ['status', 'lease', 'assignee'] })?.data ?? {};
  const lease = fields.lease as { expiries?: unknown } | undefined;
  const priority = config.priorityField === undefined ? undefined : context.data[config.priorityField];
  return {
    status: typeof fields.status === 'string' ? fields.status : null,
    blocked: config.dependencies && blockersOf(context) ? 1 : 0,
    assignee: typeof fields.assignee === 'string' ? fields.assignee : null,
    expiries: typeof lease?.expiries === 'number' ? lease.expiries : 0,
    priority: typeof priority === 'number' && Number.isSafeInteger(priority) ? priority : null,
  };
}

// refresh brings Queue's columns in line with the instance.
function refresh(context: InstanceContext<QueueConfig>): void {
  const facts = factsOf(context);
  context.columns.set({ ...facts });
}

// The operations whose changes leave every fact Queue copies as it was:
// its own refresh, and the lease's control channel.
const UNCHANGING = new Set(['Queue.refresh', 'Lease.heartbeat', 'Lease.direct', 'Lease.acknowledge']);

function states(flow: Flow): string {
  return flow.states.join(', ');
}

// checkField holds a field the config names to one of the type's own
// fields whose JSON type is one of types (null aside).
function checkField(target: ConfigTarget, at: string, field: string, types: readonly string[], what: string): void {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`${at} names "${field}", which is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  const type = (target.fieldSchemas[field] as { type?: unknown } | undefined)?.type;
  const listed = (Array.isArray(type) ? type : [type]).filter((one) => one !== 'null');
  if (listed.length === 0 || listed.some((one) => typeof one !== 'string' || !types.includes(one))) {
    throw new BehaviorConfigError(`${at} names "${field}", which is not ${what} field of ${target.type}`);
  }
}

export const queue = defineBehavior<QueueConfig>({
  declaration,

  // The configSchema holds the shape; this holds the claim to the type's
  // Workflow and the field names to its fields, and records what a claim
  // needs to know of the type's other behaviors.
  parseConfig(json, target) {
    const raw = json as { claim: { from: string[]; to: string }; priorityField?: string; match?: string[]; maxCandidates?: number };
    const flow = target.configs.Workflow as Partial<Flow> | undefined;
    // A Workflow config its own checks refuse, or none, is reported there.
    if (Array.isArray(flow?.states) && Array.isArray(flow.transitions)) {
      const workflow = flow as Flow;
      if (!workflow.states.includes(raw.claim.to)) {
        throw new BehaviorConfigError(`claim.to "${raw.claim.to}" is not a state of the type's Workflow (${states(workflow)})`);
      }
      for (const from of raw.claim.from) {
        if (!workflow.states.includes(from)) {
          throw new BehaviorConfigError(`claim.from names "${from}", which is not a state of the type's Workflow (${states(workflow)})`);
        }
        if (from === raw.claim.to) {
          throw new BehaviorConfigError(`claim.from names "${from}", the state a claim moves the instance to`);
        }
        if (!workflow.transitions.some((transition) => transition.from === from && transition.to === raw.claim.to)) {
          throw new BehaviorConfigError(`claim: no transition of the type's Workflow leads from "${from}" to "${raw.claim.to}"`);
        }
      }
    }
    if (raw.priorityField !== undefined) {
      checkField(target, 'priorityField', raw.priorityField, ['integer'], 'an integer');
    }
    for (const field of raw.match ?? []) {
      checkField(target, 'match', field, ['string', 'number', 'integer', 'boolean'], 'a scalar');
    }
    const maxExpiries = (target.configs.Lease as { maxExpiries?: unknown } | undefined)?.maxExpiries;
    return {
      claim: { from: [...raw.claim.from], to: raw.claim.to },
      ...(raw.priorityField === undefined ? {} : { priorityField: raw.priorityField }),
      match: [...(raw.match ?? [])],
      maxCandidates: raw.maxCandidates ?? DEFAULT_MAX_CANDIDATES,
      dependencies: target.behaviors.includes('Dependencies'),
      budget: target.behaviors.includes('Budget'),
      ...(typeof maxExpiries === 'number' ? { maxExpiries } : {}),
    };
  },

  configChange(before, after) {
    if (before === undefined) {
      return 'the instances that exist have no claim facts for claimNext to find them by; compose Queue before a schema has instances';
    }
    if (after === undefined) {
      return 'the claim facts and blocker references its instances hold would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'queue',
      columns: {
        status: { type: 'text' },
        blocked: { type: 'integer', notNull: true, default: 0 },
        assignee: { type: 'text' },
        expiries: { type: 'integer', notNull: true, default: 0 },
        priority: { type: 'integer' },
      },
      indexes: { claimable: ['status', 'blocked', 'priority'] },
    },
  ],

  // The lease of a claimable instance is taken by claiming it.
  guard(view, request) {
    if (request.kind === 'operation' && request.behavior === 'Lease' && request.operation === 'acquire' && request.caller !== NAME) {
      return `its lease is taken by claiming it (${NAME}'s claim), which checks that it can be claimed`;
    }
    return undefined;
  },

  operations: {
    claim(context, params) {
      const { config } = context;
      const lease = context.instances.get(context.schema, context.id, { fields: ['lease'] })?.data.lease as
        | { holder: string | null; active: boolean }
        | undefined;
      if (lease !== undefined && lease.holder !== null && !lease.active) {
        context.call('Lease', 'expire');
      }
      const now = context.instances.get(context.schema, context.id, { fields: ['status', 'blocked'] })?.data ?? {};
      if (typeof now.status !== 'string' || !config.claim.from.includes(now.status)) {
        throw vetoed(context, 'claim', `it is ${String(now.status)}, and it is claimed from ${config.claim.from.join(', ')}`);
      }
      if (config.dependencies && now.blocked === true) {
        throw vetoed(context, 'claim', 'a blocker holds it up');
      }
      const ttlMs = params.ttlMs as number | undefined;
      const taken = context.call('Lease', 'acquire', ttlMs === undefined ? {} : { ttlMs }) as Omit<ClaimRecord, 'id'>;
      // Budget's contract: reserve() with no meter reserves every meter's
      // claim amount, and a veto refuses a reservation that does not fit,
      // which refuses the claim and with it the lease. Budget settles its
      // reservations itself when the lease is released or expires.
      if (config.budget) {
        context.call('Budget', 'reserve', {});
      }
      context.call('Workflow', 'transition', { to: config.claim.to });
      return { id: context.id, token: taken.token, expiresAt: taken.expiresAt, heartbeatMs: taken.heartbeatMs };
    },

    refresh(context) {
      refresh(context);
      return {};
    },
  },

  schemaOperations: {
    claimNext(context, params) {
      const { config } = context;
      const match = (params.match ?? {}) as Record<string, string | number | boolean>;
      for (const field of Object.keys(match)) {
        if (!config.match.includes(field)) {
          throw new OperationParamsError(NAME, 'claimNext', [
            {
              path: `/match/${field}`,
              message: `claimNext on ${context.schema} matches ${config.match.length === 0 ? 'no field' : config.match.join(', ')}, not ${field}`,
            },
          ]);
        }
      }
      const where = [`status IN (${config.claim.from.map(() => '?').join(', ')})`, 'blocked = 0', '(assignee IS NULL OR assignee = ?)'];
      const values: SqlValue[] = [...config.claim.from, context.principal.subject];
      if (config.maxExpiries !== undefined) {
        where.push('expiries < ?');
        values.push(config.maxExpiries);
      }
      for (const [field, value] of Object.entries(match)) {
        where.push('json_extract(data, ?) = ?');
        values.push(`$."${field}"`, typeof value === 'boolean' ? (value ? 1 : 0) : value);
      }
      const candidates = context.sql.all(
        `SELECT id FROM ${context.sql.instances()} WHERE ${where.join(' AND ')}
         ORDER BY priority IS NULL, priority DESC, created_at, id LIMIT ?`,
        [...values, config.maxCandidates]
      );
      for (const candidate of candidates) {
        try {
          return { claimed: context.instances.invoke(context.schema, String(candidate.id), 'claim') as ClaimRecord };
        } catch (error) {
          // A veto or a conflict: this one cannot be claimed now; the next may.
          if (!(error instanceof EngineError) || (error.code !== 'vetoed' && error.code !== 'conflict')) {
            throw error;
          }
        }
      }
      return { claimed: null };
    },
  },

  afterChange(context, change) {
    if (change.kind === 'delete' || (change.kind === 'operation' && UNCHANGING.has(`${change.behavior}.${change.operation}`))) {
      return;
    }
    refresh(context);
  },

  // A blocker changed or went: the dependent's copies follow, through its
  // own refresh, as the principal who changed the blocker.
  afterReferenceChange(context, _reference: Reference) {
    context.instances.invoke(context.schema, context.id, 'refresh', {} as FrozenJSON);
  },
});
