/*
Presence, a heartbeat on an instance that stands for a worker (D16). The
instance's principalField holds the subject of the principal it stands
for, and only that principal may beat it: beat moves its deadline to
ttlMs from now. An instance not beaten by its deadline is missed. Its
initialize sets the first deadline one ttlMs after the create, so a
worker that never beats is missed too.

miss applies a miss, and only once: on an instance past its deadline that
is not missed yet, it sets missed, moves the status with onMissed when it
is one of onMissed's from states, through Workflow's transition, whose
guards run, and expires every lease the principal holds on the schemas
releaseLeases names, through each schema's expireHolder, Lease's
schema-level operation, so each expiry runs Lease's own rules and appends
its own event. The leases are the principal's, not the instance's: while
another instance of the schema stands for the same principal and is
present (not missed, before its deadline), a miss leaves them. A
transition a guard vetoes leaves the status as it is and the instance
missed all the same; any other refusal, a missing permission say, fails
the miss and leaves it as it was. Anyone who may write the instance may
call miss; on any other instance it changes nothing. A missed instance
stays missed, whatever its status, until it beats: the next beat clears
missed and sets a new deadline, which the sweep watches again, and
onBeat moves the status back from its from states.

The runner calls miss on every instance past its deadline and not
missed, every sweepMs, in its schedule miss, reading this behavior's
columns across the schema through the index on them, as the runner's
principal. That principal needs write on this schema and, for
releaseLeases, Lease's overridePermission on each schema it names, which
expireHolder asks for; a miss that fails fails the run, which the runner
retries and engine.runner.status() shows.

The guard keeps the instance to its principal: an update that changes
principalField once it holds a value is refused, whoever asks. beat
checks its caller in its handler.

configChange: any config may change except principalField, which names
where every instance's principal is held. A new ttlMs applies from each
instance's next beat. Presence can be added to a schema that has
instances, which have no deadline until their first beat, and cannot be
removed from one: the deadlines and misses they hold would stay behind
and come back if it were added again.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  defineBehavior,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceContext,
  type InstanceView,
  type OperationContext,
} from '@superschematic/engine';

import declaration from './declarations/Presence.behavior.json' with { type: 'json' };

/** How often the runner misses the instances past their deadline when the config gives no sweepMs. */
const DEFAULT_SWEEP_MS = 5000;

/** The most instances one run of the sweep misses; what is left waits for the next run. */
const MAX_SWEEP = 1000;

/** How many instances the sweep reads at a time. */
const SWEEP_BATCH = 100;

/** A move of the status a miss or a beat makes: to transition, from one of from. */
export interface PresenceTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** Presence's config, parsed: the defaults filled in. */
export interface PresenceConfig {
  readonly ttlMs: number;
  readonly principalField: string;
  readonly onMissed?: PresenceTransition;
  readonly onBeat?: PresenceTransition;
  /** The schemas whose leases the principal holds a miss expires. */
  readonly releaseLeases: readonly string[];
  readonly sweepMs: number;
}

/** The presence field. */
export interface PresenceRecord {
  /** When the instance is missed unless beaten; null for one that has never had one. */
  readonly deadline: number | null;
  readonly lastBeatAt: number | null;
  /** Whether a miss was applied and no beat came after it. */
  readonly missed: boolean;
}

const NAME = 'Presence';

/** The presence's own columns on an instance. */
interface State {
  readonly deadline: number | null;
  readonly lastBeatAt: number | null;
  readonly missed: boolean;
}

function state(view: InstanceView<PresenceConfig>): State {
  const columns = view.columns.get();
  const number = (value: unknown): number | null => (value === null || value === undefined ? null : Number(value));
  return { deadline: number(columns.deadline), lastBeatAt: number(columns.last_beat_at), missed: Number(columns.missed) === 1 };
}

// principalOf reads the subject the instance stands for; undefined when
// its principalField holds none.
function principalOf(view: InstanceView<PresenceConfig>): string | undefined {
  const value = view.data[view.config.principalField];
  return typeof value === 'string' && value !== '' ? value : undefined;
}

function vetoed(view: InstanceView<unknown>, operation: string, reason: string): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, reason);
}

// statusOf reads the instance's Workflow status as the caller; undefined
// without one.
function statusOf(context: InstanceContext<PresenceConfig>): string | undefined {
  const status = context.instances.get(context.schema, context.id, { fields: ['status'] })?.data.status;
  return typeof status === 'string' ? status : undefined;
}

// move moves the status through Workflow's transition when it is one of
// the rule's from states. A guard's veto leaves it as it is; anything
// else, a permission the caller lacks say, fails the call.
function move(context: OperationContext<PresenceConfig>, rule: PresenceTransition | undefined): void {
  if (rule === undefined) {
    return;
  }
  const status = statusOf(context);
  if (status === undefined || !rule.from.includes(status)) {
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

// presentElsewhere reports whether another instance of the schema stands
// for the principal and is present: not missed, and before its deadline.
// The leases are the principal's, so a miss of one of its instances
// leaves them while another still beats.
function presentElsewhere(context: OperationContext<PresenceConfig>, principal: string): boolean {
  const row = context.sql.get(
    `SELECT 1 AS present FROM ${context.sql.instances()} WHERE id <> ? AND missed = 0 AND deadline > ? AND json_extract(data, ?) = ? LIMIT 1`,
    [context.id, context.now, `$."${context.config.principalField}"`, principal]
  );
  return row !== undefined;
}

/** A Workflow config as the schema holds it: its states and transitions. */
interface Flow {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

// checkMove holds a status move of the config to the type's Workflow:
// every state one of its states, and a transition from each from state
// to the target.
function checkMove(flow: Flow, at: string, rule: PresenceTransition): void {
  const states = flow.states.join(', ');
  if (!flow.states.includes(rule.transition)) {
    throw new BehaviorConfigError(`${at}.transition "${rule.transition}" is not a state of the type's Workflow (${states})`);
  }
  for (const from of rule.from) {
    if (!flow.states.includes(from)) {
      throw new BehaviorConfigError(`${at}.from names "${from}", which is not a state of the type's Workflow (${states})`);
    }
    if (from === rule.transition) {
      throw new BehaviorConfigError(`${at} moves the status from "${from}" to itself`);
    }
    if (!flow.transitions.some((transition) => transition.from === from && transition.to === rule.transition)) {
      throw new BehaviorConfigError(`${at}: no transition of the type's Workflow leads from "${from}" to "${rule.transition}"`);
    }
  }
}

// checkStringField holds principalField to a string field of the type.
function checkStringField(target: ConfigTarget, field: string): void {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`principalField "${field}" is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  const type = (target.fieldSchemas[field] as { type?: unknown } | undefined)?.type;
  const types = Array.isArray(type) ? type : [type];
  if (!types.includes('string') || types.some((one) => one !== 'string' && one !== 'null')) {
    throw new BehaviorConfigError(`principalField "${field}" is not a string field of ${target.type}`);
  }
}

// checkLeases holds each schema of releaseLeases to a live schema that
// composes Lease, as the caller who defines or publishes may read it.
function checkLeases(target: ConfigTarget, schemas: readonly string[]): void {
  if (target.schemas === undefined) {
    return;
  }
  for (const name of schemas) {
    const schema = target.schemas.get(name);
    if (schema === undefined) {
      throw new BehaviorConfigError(`releaseLeases: schema ${name} has no live version; publish it first`);
    }
    if (!schema.behaviors.includes('Lease')) {
      throw new BehaviorConfigError(`releaseLeases: ${name} does not compose Lease, so it holds no lease to expire`);
    }
  }
}

export const presence = defineBehavior<PresenceConfig>({
  declaration,

  // The configSchema holds the shape; this holds the config to the type:
  // its fields, its Workflow's states, and the schemas it releases leases on.
  parseConfig(json, target) {
    const raw = json as {
      ttlMs: number;
      principalField: string;
      onMissed?: PresenceTransition;
      onBeat?: PresenceTransition;
      releaseLeases?: string[];
      sweepMs?: number;
    };
    checkStringField(target, raw.principalField);
    const workflow = target.configs.Workflow as Partial<Flow> | undefined;
    for (const [at, rule] of [
      ['onMissed', raw.onMissed],
      ['onBeat', raw.onBeat],
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
    const releaseLeases = [...(raw.releaseLeases ?? [])];
    checkLeases(target, releaseLeases);
    return {
      ttlMs: raw.ttlMs,
      principalField: raw.principalField,
      ...(raw.onMissed === undefined ? {} : { onMissed: { transition: raw.onMissed.transition, from: [...raw.onMissed.from] } }),
      ...(raw.onBeat === undefined ? {} : { onBeat: { transition: raw.onBeat.transition, from: [...raw.onBeat.from] } }),
      releaseLeases,
      sweepMs: raw.sweepMs ?? DEFAULT_SWEEP_MS,
    };
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the deadlines and misses its instances hold would stay behind and come back if it were added again';
    }
    if (before !== undefined && after !== undefined && before.principalField !== after.principalField) {
      return `principalField is where every instance holds its principal: it stays ${before.principalField}`;
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'presence',
      columns: {
        deadline: { type: 'integer' },
        last_beat_at: { type: 'integer' },
        missed: { type: 'integer', notNull: true, default: 0 },
      },
      // The sweep reads the instances not missed, oldest deadline first.
      indexes: { due: ['missed', 'deadline'] },
    },
  ],

  initialize(context) {
    context.columns.set({ deadline: context.now + context.config.ttlMs, missed: 0 });
  },

  // The instance stays the principal's: see the header.
  guard(view, request) {
    if (request.kind !== 'update') {
      return undefined;
    }
    const field = view.config.principalField;
    const before = view.data[field];
    if (before === undefined || before === null || before === request.after[field]) {
      return undefined;
    }
    return `${field} holds the principal the instance stands for, so it cannot change once set`;
  },

  operations: {
    beat(context) {
      const principal = principalOf(context);
      if (principal === undefined) {
        throw vetoed(context, 'beat', `its ${context.config.principalField} holds no principal, so no one may beat it`);
      }
      if (principal !== context.principal.subject) {
        throw vetoed(context, 'beat', 'it stands for another principal, who alone may beat it');
      }
      const deadline = context.now + context.config.ttlMs;
      context.columns.set({ deadline, last_beat_at: context.now, missed: 0 });
      move(context, context.config.onBeat);
      return { deadline };
    },

    miss(context) {
      const current = state(context);
      if (current.missed || current.deadline === null || context.now < current.deadline) {
        return { missed: false };
      }
      context.columns.set({ missed: 1 });
      move(context, context.config.onMissed);
      const principal = principalOf(context);
      if (principal !== undefined && !presentElsewhere(context, principal)) {
        for (const schema of context.config.releaseLeases) {
          context.instances.invokeSchema(schema, 'expireHolder', { holder: principal } as FrozenJSON);
        }
      }
      return { missed: true };
    },
  },

  fields: {
    presence(view): PresenceRecord {
      return state(view);
    },
  },

  schedules: {
    // miss runs miss on every instance past its deadline and not missed,
    // oldest deadline first, through the index on the two columns, at
    // most MAX_SWEEP a run. Each miss sets missed, so the next batch
    // starts after it; one that changes nothing ends the run, so a run
    // never reads the same instance twice.
    miss: {
      everyMs: (config: PresenceConfig) => config.sweepMs,
      run(context) {
        const relation = context.sql.instances();
        let swept = 0;
        while (swept < MAX_SWEEP) {
          const due = context.sql.all(`SELECT id FROM ${relation} WHERE missed = 0 AND deadline <= ? ORDER BY deadline, id LIMIT ?`, [
            context.now,
            Math.min(SWEEP_BATCH, MAX_SWEEP - swept),
          ]);
          for (const row of due) {
            const result = context.instances.invoke(context.schema, String(row.id), 'miss') as { missed: boolean };
            if (!result.missed) {
              return;
            }
          }
          swept += due.length;
          if (due.length < SWEEP_BATCH) {
            return;
          }
        }
      },
    },
  },
});
