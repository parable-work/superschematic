/*
Budget, reserve-then-settle budgets on an instance (D16), in units the
deployment names. Each meter of the config has, per instance, a row of
Budget's own table: what the instance has used, what it has reserved, the
limit setLimit gave it, and for a daily meter the UTC day its usage counts
from. reserved is the instance's own reservation, the one its claims made,
plus what it holds for the instances inside it; the own part is kept
apart, with the lease token it was made under, so settling releases
exactly that.

A meter's limit is the config's limit, or the instance's limitField, or
the one setLimit set; with none, the meter counts without a limit of its
own. A reservation fits while used plus reserved plus the amount is
within the limit.

Scopes. A meter may name a scope: a link of the type's Links config whose
target encloses the instance, a pool its work draws on, say. The target's
schema composes Budget with the same meter, which parseConfig checks when
the schema is defined. Budget never writes another instance's rows (D16):
the instance invokes an operation on its scope, reserveFor, settleFor or
recordUsageFor, which applies the scope's own check, records what the
scope holds for the instance, writes the scope's rows, appends the scope's
own event and passes the change on up the scope's own scope. All of it
runs in the caller's transaction, so a reservation one scope refuses rolls
back the whole chain. The invoked operations run as the caller, so the
access policy and the scope's own guards are asked as for any operation.

The scope operations cannot free budget an instance still holds. A scope
reads the instance through its budget field before it releases anything,
and releases at most what it holds for the instance beyond what the
instance still has reserved; it holds no more for an instance than the
instance has reserved; and it takes reservations and usage only from an
instance whose scope link for the meter points at it. Calling them by
hand can make a scope agree with its instances, never undercount them. A
scope link does not move while the instance has a reservation of a meter
through it (the guard). The row records the scope that holds the
instance's reservation, and releases go there: a deleted instance's, and
after a new version moved the meter's scope, the part usage draws down. A
reservation through another scope waits until what is held at the first
is settled.

Usage is never refused. recordUsage adds the amount to used, releases the
part of it the instance's own reservation covers, min(amount, own), and
releases exactly that part at each scope, so usage beyond a reservation
never eats the reservations other instances hold in a scope. The usage
counts in every scope too. Each instance of the chain whose used is over
its limit afterwards is reported in the result; with onExceeded, the
holder of the instance's active lease also gets a directive, through
Lease's direct, once per meter per lease token.

Lease. On a type that composes Lease, a reservation is made under the
active lease and lives as long as it: reserve is refused while no lease
is active, and after a caller's Lease or Queue operation, and before each
reserve, the reservations of a lease that is no longer the active one are
settled. That covers a release, an expiry, and an acquire over a lapsed
lease, which applies its expiry inside the acquire. Deleting the instance
settles everything it reserved and holds.

A daily meter's usage counts from the start of the UTC day. A read never
writes: a meter whose day has passed reads as used 0, and the next write
of its row starts the new day first. The engine's clock gives the time,
for reads and writes alike.

configChange: a meter cannot be removed, since instances and scopes hold
it; limits, reservations, scopes, resets and the rest may change. A
reservation is always released where it was held. Budget can be added to
a schema that has instances, whose rows start empty, and cannot be
removed from one: their budgets, and what scopes hold for them, would
stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  type BehaviorScope,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceContext,
  type InstanceRecord,
  type InstanceView,
  type OperationContext,
  type Row,
  type SqlValue,
} from '@superschematic/engine';

import declaration from './declarations/Budget.behavior.json' with { type: 'json' };

/** One meter of a Budget config, parsed. */
export interface BudgetMeter {
  readonly limit?: number;
  readonly limitField?: string;
  readonly reserve?: number;
  readonly reserveField?: string;
  /** The Links link whose target is the enclosing scope. */
  readonly scope?: string;
  /** Whether used starts again at 0 at each UTC day. */
  readonly daily: boolean;
}

/** Budget's config, parsed. */
export interface BudgetConfig {
  readonly meters: Readonly<Record<string, BudgetMeter>>;
  readonly limitPermission?: string;
  readonly onExceeded?: { readonly direct: string };
  /** Whether the type composes Lease, whose active lease a reservation lives within. */
  readonly leased: boolean;
}

/** One meter, as the budget field holds it. */
export interface MeterRecord {
  readonly used: number;
  /** The instance's own reservation and what it holds for the instances inside it. */
  readonly reserved: number;
  /** null when the meter has no limit. */
  readonly limit: number | null;
  /** limit minus used and reserved, below 0 once usage passed the limit; null without a limit. */
  readonly remaining: number | null;
}

/** An instance of a chain whose usage of a meter is over its limit. */
export interface Overrun {
  readonly schema: string;
  readonly id: string;
  readonly used: number;
  readonly limit: number;
}

const NAME = 'Budget';
const DAY_MS = 86_400_000;
const COLUMNS = 'meter, used, reserved, limit_value, period_start, reservation, token, directed, scope_schema, scope_id';

/** An instance, by schema and id. */
interface Target {
  readonly schema: string;
  readonly id: string;
}

/** A meter's row, or the values of one not written yet. */
interface Meter {
  readonly used: number;
  readonly reserved: number;
  readonly limitValue: number | null;
  readonly periodStart: number | null;
  /** The instance's own reservation, a part of reserved. */
  readonly reservation: number;
  /** The lease token the own reservation was made under. */
  readonly token: number | null;
  /** The lease token onExceeded's directive was last sent under. */
  readonly directed: number | null;
  /** Where the reservation is held up the chain: the scope it was last passed to. */
  readonly scope: Target | undefined;
}

const EMPTY: Meter = { used: 0, reserved: 0, limitValue: null, periodStart: null, reservation: 0, token: null, directed: null, scope: undefined };

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

function own<T>(record: Readonly<Record<string, T>> | undefined, name: string): T | undefined {
  return record !== undefined && Object.prototype.hasOwnProperty.call(record, name) ? record[name] : undefined;
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

function dayStart(now: number): number {
  return Math.floor(now / DAY_MS) * DAY_MS;
}

function numberOrNull(value: SqlValue | undefined): number | null {
  return value === null || value === undefined ? null : Number(value);
}

function meterOf(row: Row): Meter {
  return {
    used: Number(row.used),
    reserved: Number(row.reserved),
    limitValue: numberOrNull(row.limit_value),
    periodStart: numberOrNull(row.period_start),
    reservation: Number(row.reservation),
    token: numberOrNull(row.token),
    directed: numberOrNull(row.directed),
    scope: row.scope_schema === null ? undefined : { schema: String(row.scope_schema), id: String(row.scope_id) },
  };
}

// rows reads the instance's meter rows, by meter.
function rows(view: InstanceView<BudgetConfig>): Map<string, Meter> {
  const found = view.sql.all(`SELECT ${COLUMNS} FROM ${view.sql.table('meters')} WHERE namespace = ? AND schema = ? AND id = ?`, key(view));
  return new Map(found.map((row) => [String(row.meter), meterOf(row)]));
}

function rowOf(view: InstanceView<BudgetConfig>, meter: string): Meter {
  const row = view.sql.get(`SELECT ${COLUMNS} FROM ${view.sql.table('meters')} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ?`, [
    ...key(view),
    meter,
  ]);
  return row === undefined ? EMPTY : meterOf(row);
}

// rolled reports whether a daily meter's day has passed since its usage
// started counting, so its used is 0 now.
function rolled(view: InstanceView<BudgetConfig>, spec: BudgetMeter, meter: Meter): boolean {
  return spec.daily && meter.periodStart !== null && meter.periodStart < dayStart(view.now);
}

function usedNow(view: InstanceView<BudgetConfig>, spec: BudgetMeter, meter: Meter): number {
  return rolled(view, spec, meter) ? 0 : meter.used;
}

/** limitOf is the one rule for a meter's limit on an instance: its limitField, setLimit's, or the config's; null for none. */
function limitOf(view: InstanceView<BudgetConfig>, spec: BudgetMeter, meter: Meter): number | null {
  if (spec.limitField !== undefined) {
    const value = view.data[spec.limitField];
    return isCount(value) ? value : null;
  }
  return meter.limitValue ?? spec.limit ?? null;
}

function recordOf(view: InstanceView<BudgetConfig>, spec: BudgetMeter, meter: Meter): MeterRecord {
  const used = usedNow(view, spec, meter);
  const limit = limitOf(view, spec, meter);
  return { used, reserved: meter.reserved, limit, remaining: limit === null ? null : limit - used - meter.reserved };
}

// load reads a meter's row for a write, making it when the instance has
// none yet and starting a daily meter's new day first: the one place a
// row is made or its day moves, so a read never writes.
function load(context: InstanceContext<BudgetConfig>, meter: string): Meter {
  const spec = context.config.meters[meter];
  const table = context.sql.table('meters');
  const today = dayStart(context.now);
  context.sql.run(`INSERT INTO ${table} (namespace, schema, id, meter, period_start) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, [
    ...key(context),
    meter,
    spec.daily ? today : null,
  ]);
  const row = rowOf(context, meter);
  if (spec.daily && (row.periodStart === null || row.periodStart < today)) {
    const used = row.periodStart === null ? row.used : 0;
    save(context, meter, { used, period_start: today });
    return { ...row, used, periodStart: today };
  }
  return row;
}

function save(context: InstanceContext<BudgetConfig>, meter: string, values: Readonly<Record<string, SqlValue>>): void {
  const names = Object.keys(values);
  context.sql.run(
    `UPDATE ${context.sql.table('meters')} SET ${names.map((name) => `${name} = ?`).join(', ')} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ?`,
    [...names.map((name) => values[name]), ...key(context), meter]
  );
}

function vetoed(view: InstanceView<unknown>, operation: string, reason: string): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, reason);
}

function forbidden(view: InstanceView<unknown>, what: string, permission: string): EngineError {
  return new EngineError('forbidden', `${view.principal.subject} may not ${what} ${view.schema} ${view.id}: it needs permission ${permission}`);
}

// specOf returns a meter's spec, or refuses a name the config does not give.
function specOf(scope: BehaviorScope<BudgetConfig>, operation: string, meter: string): BudgetMeter {
  const spec = own(scope.config.meters, meter);
  if (spec === undefined) {
    throw new OperationParamsError(NAME, operation, [
      { path: '/meter', message: `${scope.schema} has no meter ${meter} (its meters: ${Object.keys(scope.config.meters).join(', ')})` },
    ]);
  }
  return spec;
}

// claimAmount is what reserve takes of a meter when it is given no
// meter: the config's reserve, or the instance's positive reserveField.
function claimAmount(view: InstanceView<BudgetConfig>, spec: BudgetMeter): number | undefined {
  if (spec.reserveField !== undefined) {
    const value = view.data[spec.reserveField];
    return isCount(value) && value > 0 ? value : undefined;
  }
  return spec.reserve;
}

// scopeOf reads where a meter's scope link points now, through the
// instance's links field, as the caller; undefined when the meter has no
// scope or the link is not set.
function scopeOf(view: InstanceView<BudgetConfig>, meter: string): Target | undefined {
  const link = view.config.meters[meter].scope;
  if (link === undefined) {
    return undefined;
  }
  const links = view.instances.get(view.schema, view.id, { fields: ['links'] })?.data.links as Readonly<Record<string, Target>> | undefined;
  const target = own(links, link);
  return target === undefined ? undefined : { schema: target.schema, id: target.id };
}

/** The instance's lease, read through Lease's field; undefined on a type without Lease. */
interface LeaseState {
  readonly token: number;
  readonly active: boolean;
}

function leaseOf(view: InstanceView<BudgetConfig>): LeaseState | undefined {
  if (!view.config.leased) {
    return undefined;
  }
  const lease = view.instances.get(view.schema, view.id, { fields: ['lease'] })?.data.lease as { token?: unknown; active?: unknown } | undefined;
  return lease === undefined ? undefined : { token: Number(lease.token), active: lease.active === true };
}

function reservedIn(record: InstanceRecord | undefined, meter: string): number {
  const budget = record?.data.budget as Readonly<Record<string, MeterRecord>> | undefined;
  return own(budget, meter)?.reserved ?? 0;
}

// innerOf reads an instance that asks a scope operation of this one: its
// reservation of the meter, through its budget field. It refuses one whose
// schema's meter does not draw on a scope link that points here.
function innerOf(context: OperationContext<BudgetConfig>, operation: string, meter: string, inner: Target): number {
  const config = context.schemas.config(inner.schema, NAME) as { meters?: Readonly<Record<string, { scope?: string }>> } | undefined;
  const link = own(config?.meters, meter)?.scope;
  const record = link === undefined ? undefined : context.instances.get(inner.schema, inner.id, { fields: ['budget', 'links'] });
  const target = link === undefined ? undefined : own(record?.data.links as Readonly<Record<string, Target>> | undefined, link);
  if (target === undefined || target.schema !== context.schema || target.id !== context.id) {
    throw new OperationParamsError(NAME, operation, [
      { path: '/id', message: `${inner.schema} ${inner.id} does not draw meter ${meter} from ${context.schema} ${context.id}` },
    ]);
  }
  return reservedIn(record, meter);
}

function heldFor(view: InstanceView<BudgetConfig>, meter: string, inner: Target): number {
  const row = view.sql.get(
    `SELECT amount FROM ${view.sql.table('holds')} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ? AND inner_schema = ? AND inner_id = ?`,
    [...key(view), meter, inner.schema, inner.id]
  );
  return row === undefined ? 0 : Number(row.amount);
}

function hold(context: InstanceContext<BudgetConfig>, meter: string, inner: Target, amount: number): void {
  const table = context.sql.table('holds');
  if (amount === 0) {
    context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ? AND inner_schema = ? AND inner_id = ?`, [
      ...key(context),
      meter,
      inner.schema,
      inner.id,
    ]);
    return;
  }
  context.sql.run(
    `INSERT INTO ${table} (namespace, schema, id, meter, inner_schema, inner_id, amount) VALUES (?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT (namespace, schema, id, meter, inner_schema, inner_id) DO UPDATE SET amount = excluded.amount`,
    [...key(context), meter, inner.schema, inner.id, amount]
  );
}

// fit refuses a reservation the meter's limit cannot take.
function fit(context: InstanceContext<BudgetConfig>, operation: string, meter: string, row: Meter, amount: number): void {
  const spec = context.config.meters[meter];
  const limit = limitOf(context, spec, row);
  if (limit !== null && row.used + row.reserved + amount > limit) {
    const remaining = Math.max(0, limit - row.used - row.reserved);
    throw vetoed(context, operation, `meter ${meter} has ${remaining} of its limit ${limit} left, not ${amount}`);
  }
}

// passUp hands a change of a meter on to a scope, as an operation of the
// scope that names this instance: to where the reservation is held for a
// release, to where the scope link points now otherwise. Nothing without
// a scope.
function passUp(context: InstanceContext<BudgetConfig>, meter: string, operation: string, params: Record<string, unknown>, target: Target | undefined): unknown {
  if (target === undefined) {
    return undefined;
  }
  return context.instances.invoke(target.schema, target.id, operation, { meter, schema: context.schema, id: context.id, ...params } as FrozenJSON);
}

function same(a: Target | undefined, b: Target | undefined): boolean {
  return a !== undefined && b !== undefined && a.schema === b.schema && a.id === b.id;
}

// take reserves an amount of a meter here and up the chain. Everything
// the instance has reserved is held at one scope: a reservation through
// another, after a new version moved the meter's scope, waits until what
// is held at the first is settled.
function take(context: InstanceContext<BudgetConfig>, operation: string, meter: string, amount: number, own: boolean, token: number | null): Meter {
  const row = load(context, meter);
  fit(context, operation, meter, row, amount);
  const scope = scopeOf(context, meter);
  if (scope !== undefined && row.reserved > 0 && row.scope !== undefined && !same(row.scope, scope)) {
    throw vetoed(
      context,
      operation,
      `meter ${meter} has ${row.reserved} reserved through ${row.scope.schema} ${row.scope.id}; settle it before reserving through ${scope.schema} ${scope.id}`
    );
  }
  save(context, meter, {
    reserved: row.reserved + amount,
    ...(own ? { reservation: row.reservation + amount, token } : {}),
    ...(scope === undefined ? {} : { scope_schema: scope.schema, scope_id: scope.id }),
  });
  passUp(context, meter, 'reserveFor', { amount }, scope);
  return row;
}

// release releases an amount of reserved here, and passes it on to the
// scope that holds it.
function release(context: InstanceContext<BudgetConfig>, meter: string, amount: number, ownPart: number): void {
  if (amount === 0) {
    return;
  }
  const row = load(context, meter);
  const reservation = row.reservation - ownPart;
  save(context, meter, { reserved: Math.max(0, row.reserved - amount), reservation, ...(reservation === 0 ? { token: null } : {}) });
  passUp(context, meter, 'settleFor', { amount }, row.scope);
}

// settleOwn releases the instance's own reservation of a meter.
function settleOwn(context: InstanceContext<BudgetConfig>, meter: string): number {
  const amount = rowOf(context, meter).reservation;
  release(context, meter, amount, amount);
  return amount;
}

// settleEnded settles the own reservations made under a lease that is no
// longer the active one, after a Lease or Queue operation and before a
// reserve. The lease is read only when one is outstanding.
function settleEnded(context: InstanceContext<BudgetConfig>): void {
  const outstanding = context.sql.all(
    `SELECT meter, token FROM ${context.sql.table('meters')} WHERE namespace = ? AND schema = ? AND id = ? AND reservation > 0 ORDER BY meter`,
    key(context)
  );
  if (outstanding.length === 0) {
    return;
  }
  const lease = leaseOf(context);
  const active = lease?.active === true ? lease.token : undefined;
  for (const row of outstanding) {
    if (numberOrNull(row.token) !== active && own(context.config.meters, String(row.meter)) !== undefined) {
      settleOwn(context, String(row.meter));
    }
  }
}

// direct sends onExceeded's directive to the holder of the active lease,
// once per meter per lease token. A refusal of Lease's direct, a
// permission the caller lacks say, sends none; usage is never refused.
function direct(context: OperationContext<BudgetConfig>, meter: string, overrun: Overrun): boolean {
  const onExceeded = context.config.onExceeded;
  const lease = onExceeded === undefined ? undefined : leaseOf(context);
  if (onExceeded === undefined || lease === undefined || !lease.active || rowOf(context, meter).directed === lease.token) {
    return false;
  }
  try {
    context.call('Lease', 'direct', {
      name: onExceeded.direct,
      data: { meter, used: overrun.used, limit: overrun.limit, scope: { schema: overrun.schema, id: overrun.id } },
    } as FrozenJSON);
  } catch (error) {
    if (error instanceof BehaviorVetoError || (error instanceof EngineError && error.code === 'forbidden')) {
      return false;
    }
    throw error;
  }
  save(context, meter, { directed: lease.token });
  return true;
}

// passUsage hands usage of a meter on to the scope its link points at
// now, with the part of the reservation it released, which is released
// where the reservation is held; the two are one scope unless a new
// version moved the meter's scope. It returns the scopes' overruns.
function passUsage(context: InstanceContext<BudgetConfig>, meter: string, amount: number, released: number, held: Target | undefined): Overrun[] {
  const scope = scopeOf(context, meter);
  let carried = released;
  if (released > 0 && held !== undefined && !same(held, scope)) {
    passUp(context, meter, 'settleFor', { amount: released }, held);
    carried = 0;
  }
  const upstream = passUp(context, meter, 'recordUsageFor', { amount, released: carried }, scope) as { overruns: Overrun[] } | undefined;
  return upstream?.overruns ?? [];
}

// count adds usage of a meter here, releases freed of what is reserved,
// and reports the instance when it is then over its limit.
function count(context: InstanceContext<BudgetConfig>, meter: string, amount: number, freed: number, ownPart: number): { row: Meter; overrun?: Overrun } {
  const row = load(context, meter);
  const used = row.used + amount;
  const reservation = row.reservation - ownPart;
  save(context, meter, { used, reserved: Math.max(0, row.reserved - freed), reservation, ...(reservation === 0 ? { token: null } : {}) });
  const limit = limitOf(context, context.config.meters[meter], row);
  return { row: { ...row, used }, ...(limit !== null && used > limit ? { overrun: { schema: context.schema, id: context.id, used, limit } } : {}) };
}

function innerTarget(params: FrozenJSON): Target {
  return { schema: params.schema as string, id: params.id as string };
}

// checkIntegerField holds limitField and reserveField to an integer field of the type.
function checkIntegerField(target: ConfigTarget, at: string, field: string): void {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`${at} "${field}" is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  const type = (target.fieldSchemas[field] as { type?: unknown } | undefined)?.type;
  const types = Array.isArray(type) ? type : [type];
  if (!types.includes('integer') || types.some((one) => one !== 'integer' && one !== 'null')) {
    throw new BehaviorConfigError(`${at} "${field}" is not an integer field of ${target.type}`);
  }
}

// checkScope holds a meter's scope to a link of the type's Links config
// and, when the schema is defined or published, the link's target schema
// to one that composes Budget with the same meter.
function checkScope(target: ConfigTarget, meter: string, link: string): void {
  const at = `meter ${meter}: scope ${link}`;
  if (!target.behaviors.includes('Links')) {
    throw new BehaviorConfigError(`${at} is a link of Links, which the type does not list`);
  }
  const links = (target.configs.Links as { links?: Readonly<Record<string, { schema?: unknown }>> } | undefined)?.links;
  const spec = own(links, link);
  if (spec === undefined) {
    // A Links config of the wrong shape is Links' to refuse.
    if (links !== undefined && typeof links === 'object') {
      throw new BehaviorConfigError(`${at} is not a link of the type's Links config (its links: ${Object.keys(links).join(', ')})`);
    }
    return;
  }
  if (target.schemas === undefined || typeof spec.schema !== 'string') {
    return;
  }
  const scope = target.schemas.get(spec.schema);
  if (scope === undefined) {
    throw new BehaviorConfigError(`${at}: schema ${spec.schema}, which the link points at, has no live version; publish it first`);
  }
  if (!scope.behaviors.includes(NAME)) {
    throw new BehaviorConfigError(`${at}: ${spec.schema}, which the link points at, does not compose Budget`);
  }
  const meters = (scope.configs[NAME] as { meters?: Readonly<Record<string, unknown>> } | undefined)?.meters;
  if (own(meters, meter) === undefined) {
    throw new BehaviorConfigError(`${at}: ${spec.schema}, which the link points at, has no meter ${meter} (its meters: ${Object.keys(meters ?? {}).join(', ')})`);
  }
}

export const budget = defineBehavior<BudgetConfig>({
  declaration,

  // The configSchema holds the shape; this holds the config to the type:
  // its fields, its Links config and the schemas its scopes point at.
  parseConfig(json, target) {
    const raw = json as {
      meters: Record<string, { limit?: number; limitField?: string; reserve?: number; reserveField?: string; scope?: string; reset?: 'daily' }>;
      limitPermission?: string;
      onExceeded?: { direct: string };
    };
    const meters: Record<string, BudgetMeter> = {};
    for (const [name, meter] of Object.entries(raw.meters)) {
      if (meter.limitField !== undefined) {
        checkIntegerField(target, `meter ${name}: limitField`, meter.limitField);
      }
      if (meter.reserveField !== undefined) {
        checkIntegerField(target, `meter ${name}: reserveField`, meter.reserveField);
      }
      if (meter.scope !== undefined) {
        checkScope(target, name, meter.scope);
      }
      // A meter's name is camelCase (configSchema), so it is never __proto__.
      meters[name] = {
        ...(meter.limit === undefined ? {} : { limit: meter.limit }),
        ...(meter.limitField === undefined ? {} : { limitField: meter.limitField }),
        ...(meter.reserve === undefined ? {} : { reserve: meter.reserve }),
        ...(meter.reserveField === undefined ? {} : { reserveField: meter.reserveField }),
        ...(meter.scope === undefined ? {} : { scope: meter.scope }),
        daily: meter.reset === 'daily',
      };
    }
    if (raw.onExceeded !== undefined && !target.behaviors.includes('Lease')) {
      throw new BehaviorConfigError("onExceeded sends a directive through Lease's direct, which the type does not list");
    }
    return {
      meters,
      ...(raw.limitPermission === undefined ? {} : { limitPermission: raw.limitPermission }),
      ...(raw.onExceeded === undefined ? {} : { onExceeded: { direct: raw.onExceeded.direct } }),
      leased: target.behaviors.includes('Lease'),
    };
  },

  configChange(before, after) {
    if (before === undefined) {
      return undefined;
    }
    if (after === undefined) {
      return 'the budgets its instances hold, and what enclosing scopes hold for them, would stay behind';
    }
    for (const name of Object.keys(before.meters)) {
      if (own(after.meters, name) === undefined) {
        return `meter ${name} is gone, and its instances and the scopes they draw on may hold it`;
      }
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'budget',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('meters')} (
          namespace    TEXT    NOT NULL,
          schema       TEXT    NOT NULL,
          id           TEXT    NOT NULL,
          meter        TEXT    NOT NULL,
          used         INTEGER NOT NULL DEFAULT 0,
          reserved     INTEGER NOT NULL DEFAULT 0,
          limit_value  INTEGER,
          period_start INTEGER,
          reservation  INTEGER NOT NULL DEFAULT 0,
          token        INTEGER,
          directed     INTEGER,
          scope_schema TEXT,
          scope_id     TEXT,
          PRIMARY KEY (namespace, schema, id, meter)
        ) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('holds')} (
          namespace    TEXT    NOT NULL,
          schema       TEXT    NOT NULL,
          id           TEXT    NOT NULL,
          meter        TEXT    NOT NULL,
          inner_schema TEXT    NOT NULL,
          inner_id     TEXT    NOT NULL,
          amount       INTEGER NOT NULL,
          PRIMARY KEY (namespace, schema, id, meter, inner_schema, inner_id)
        ) STRICT`);
      },
    },
  ],

  guard(view, request) {
    // A scope link stays put while a reservation is held through it, so
    // a reservation is released where it is held.
    if (request.kind === 'operation' && request.behavior === 'Links' && (request.operation === 'link' || request.operation === 'unlink')) {
      const link = request.params.name;
      for (const [meter, spec] of Object.entries(view.config.meters)) {
        const reserved = spec.scope === link ? rowOf(view, meter).reserved : 0;
        if (reserved > 0) {
          return `meter ${meter} has ${reserved} reserved through link ${String(link)}; settle it before the link changes`;
        }
      }
      return undefined;
    }
    // limitField changes as a limit does: with limitPermission, and never
    // below what the instance has used and reserved. setLimit's own
    // update() was asked already.
    if (request.kind === 'update' && request.caller !== NAME) {
      for (const [meter, spec] of Object.entries(view.config.meters)) {
        const field = spec.limitField;
        if (field === undefined || !Object.prototype.hasOwnProperty.call(request.patch, field)) {
          continue;
        }
        const permission = view.config.limitPermission;
        if (permission === undefined) {
          return `${field} holds the limit of meter ${meter}, which changes only with limitPermission, and the config names none`;
        }
        if (!view.can(permission)) {
          throw forbidden(view, `change ${field}, the limit of meter ${meter}, of`, permission);
        }
        const limit = request.after[field];
        const row = rowOf(view, meter);
        const committed = usedNow(view, spec, row) + row.reserved;
        if (isCount(limit) && limit < committed) {
          return `meter ${meter} has ${committed} used and reserved, more than the limit ${limit}`;
        }
      }
    }
    return undefined;
  },

  operations: {
    reserve(context, params) {
      const meter = params.meter as string | undefined;
      const amount = params.amount as number | undefined;
      if (amount !== undefined && meter === undefined) {
        throw new OperationParamsError(NAME, 'reserve', [{ path: '/amount', message: 'an amount is reserved of one meter, which meter names' }]);
      }
      let token: number | null = null;
      if (context.config.leased) {
        const lease = leaseOf(context);
        if (lease === undefined || !lease.active) {
          throw vetoed(context, 'reserve', 'no lease is active, and on a type that composes Lease a reservation is made under the active lease');
        }
        settleEnded(context);
        token = lease.token;
      }
      const plan: Array<[string, number]> = [];
      if (meter !== undefined) {
        const spec = specOf(context, 'reserve', meter);
        const wanted = amount ?? claimAmount(context, spec);
        if (wanted === undefined) {
          throw new OperationParamsError(NAME, 'reserve', [{ path: '/amount', message: `meter ${meter} has no configured reservation, so reserve takes an amount` }]);
        }
        plan.push([meter, wanted]);
      } else {
        for (const [name, spec] of Object.entries(context.config.meters)) {
          const wanted = claimAmount(context, spec);
          if (wanted !== undefined) {
            plan.push([name, wanted]);
          }
        }
      }
      const reserved: Record<string, number> = {};
      for (const [name, wanted] of plan) {
        take(context, 'reserve', name, wanted, true, token);
        reserved[name] = wanted;
      }
      return { reserved };
    },

    recordUsage(context, params) {
      const meter = params.meter as string;
      specOf(context, 'recordUsage', meter);
      const amount = params.amount as number;
      const drawn = Math.min(amount, rowOf(context, meter).reservation);
      const counted = count(context, meter, amount, drawn, drawn);
      const overruns: Overrun[] = counted.overrun === undefined ? [] : [counted.overrun];
      overruns.push(...passUsage(context, meter, amount, drawn, counted.row.scope));
      const directed = overruns.length > 0 && direct(context, meter, overruns[0]);
      return { meter, used: counted.row.used, released: drawn, overruns, directed };
    },

    settle(context, params) {
      const meter = params.meter as string | undefined;
      if (meter !== undefined) {
        specOf(context, 'settle', meter);
      }
      const released: Record<string, number> = {};
      for (const name of meter === undefined ? Object.keys(context.config.meters) : [meter]) {
        released[name] = settleOwn(context, name);
      }
      return { released };
    },

    setLimit(context, params) {
      const permission = context.config.limitPermission;
      if (permission === undefined) {
        throw vetoed(context, 'setLimit', 'its config names no limitPermission, which changing a limit needs');
      }
      if (!context.can(permission)) {
        throw forbidden(context, 'change a limit of', permission);
      }
      const meter = params.meter as string;
      const spec = specOf(context, 'setLimit', meter);
      const limit = params.limit as number;
      const row = rowOf(context, meter);
      const previous = limitOf(context, spec, row);
      const committed = usedNow(context, spec, row) + row.reserved;
      if (limit < committed) {
        throw vetoed(context, 'setLimit', `meter ${meter} has ${committed} used and reserved, more than the limit ${limit}`);
      }
      if (spec.limitField !== undefined) {
        context.update({ [spec.limitField]: limit });
      } else {
        load(context, meter);
        save(context, meter, { limit_value: limit });
      }
      return { meter, limit, previous };
    },

    reserveFor(context, params) {
      const meter = params.meter as string;
      specOf(context, 'reserveFor', meter);
      const inner = innerTarget(params);
      const amount = params.amount as number;
      const reserved = innerOf(context, 'reserveFor', meter, inner);
      const held = heldFor(context, meter, inner);
      if (held + amount > reserved) {
        throw vetoed(
          context,
          'reserveFor',
          `${inner.schema} ${inner.id} has ${reserved} of meter ${meter} reserved and ${held} of it is held here, so ${amount} more is not its to hold`
        );
      }
      take(context, 'reserveFor', meter, amount, false, null);
      hold(context, meter, inner, held + amount);
      return { held: held + amount };
    },

    settleFor(context, params) {
      const meter = params.meter as string;
      specOf(context, 'settleFor', meter);
      const inner = innerTarget(params);
      const held = heldFor(context, meter, inner);
      const still = held === 0 ? 0 : reservedIn(context.instances.get(inner.schema, inner.id, { fields: ['budget'] }), meter);
      const released = Math.min(params.amount as number, Math.max(0, held - still));
      if (released > 0) {
        hold(context, meter, inner, held - released);
        release(context, meter, released, 0);
      }
      return { released };
    },

    recordUsageFor(context, params) {
      const meter = params.meter as string;
      specOf(context, 'recordUsageFor', meter);
      const inner = innerTarget(params);
      const amount = params.amount as number;
      const still = innerOf(context, 'recordUsageFor', meter, inner);
      const held = heldFor(context, meter, inner);
      const freed = Math.min(params.released as number, Math.max(0, held - still));
      if (freed > 0) {
        hold(context, meter, inner, held - freed);
      }
      const counted = count(context, meter, amount, freed, 0);
      const overruns: Overrun[] = counted.overrun === undefined ? [] : [counted.overrun];
      overruns.push(...passUsage(context, meter, amount, freed, counted.row.scope));
      return { released: freed, overruns };
    },
  },

  fields: {
    budget(view) {
      const found = rows(view);
      const out: Record<string, MeterRecord> = {};
      for (const [meter, spec] of Object.entries(view.config.meters)) {
        out[meter] = recordOf(view, spec, found.get(meter) ?? EMPTY);
      }
      return out;
    },
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      // Everything the instance reserved and holds goes back where it is
      // held; the scope reads the instance as gone, so nothing stays held.
      for (const [meter, row] of rows(context)) {
        if (row.reserved > 0) {
          passUp(context, meter, 'settleFor', { amount: row.reserved }, row.scope);
        }
      }
      for (const table of ['meters', 'holds']) {
        context.sql.run(`DELETE FROM ${context.sql.table(table)} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      }
      return;
    }
    if (change.kind === 'operation' && context.config.leased && (change.behavior === 'Lease' || change.behavior === 'Queue')) {
      settleEnded(context);
    }
  },
});
