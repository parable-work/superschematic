// The recorded scenarios are the specification (D103), and development
// mode is a layout the core's host reads (D117): every transcript
// rules-dev's replay names (runtime/engine/rust/rules-dev/tests/
// transcript.rs's TRANSCRIPTS, with each one's floor, read from that file
// so the two lists are one), replayed through the handler's bridge over
// this host's development layout on bun:sqlite, as that replay runs it:
// each scenario's database a store of its own; each command over every
// type of it, the command's at the version live at it and each other at
// the version the scenario last made live, its refusal or its result
// compared whole, and the row it names read back after it and compared,
// with its version when the transcript holds one, its sub-table rows, its
// armed deadlines, the other rows its change set wrote, and the events it
// wrote. A check runs on the core alone, its type's rows read from the
// store, and a publish's drops the shared tables' rows of each use it no
// longer lists; a raw step writes owned fields past the bridge, moving the
// row's version as a table's trigger does.
import { afterAll, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Json, JsonObject, StoreWrite, Unit } from '../dist/index.js';
import { RawJson, type SqliteStore } from '../dist/index.js';
import { checkout, handler, same, scratch, sqlite } from './support.ts';

type Use = { behavior: string; anchor: 'type' | { field: string } };
type Spec = { name: string; uses?: Use[] } & JsonObject;
type Command = { action: { kind: string; id?: string; key?: string } & JsonObject } & JsonObject;
type Expect = {
  refused?: Json;
  result?: Json;
  key?: string;
  row?: Json;
  version?: number;
  subRows?: Json;
  deadlines?: Json;
  others?: Json;
  events?: Json[];
};
type Step =
  | { kind: 'command'; step: number; call: string; database: string; type: string; version: number; command: Command; expect: Expect }
  | { kind: 'check'; step: number; call: string; database: string; spec: Spec; command: Command; expect: Expect }
  | { kind: 'raw'; step: number; call: string; database: string; type: string; key: string; owned: Record<string, JsonObject> };
type Scenario = { name: string; types?: { name: string; versions?: Spec[] }[]; steps?: Step[] };
type Transcript = { declarations?: object[]; scenarios: Scenario[] };

/** rules-dev's TRANSCRIPTS: each transcript's name and floor. */
function transcripts(): [string, number][] {
  const source = readFileSync(join(checkout, 'runtime/engine/rust/rules-dev/tests/transcript.rs'), 'utf8');
  const list = source.match(/const TRANSCRIPTS: \[\(&str, usize\); \d+\] = \[([\s\S]*?)\];/)?.[1];
  if (list === undefined) throw new Error('rules-dev/tests/transcript.rs has no TRANSCRIPTS');
  return [...list.matchAll(/\("([a-z0-9-]+)",\s*(\d+)\)/g)].map(match => [match[1]!, Number(match[2])]);
}

function transcript(name: string): Transcript {
  return JSON.parse(readFileSync(join(checkout, `runtime/engine/testdata/rules/${name}.transcript.json`), 'utf8')) as Transcript;
}

// The handler runs the declarative behaviors the transcripts declare (D103's
// step 5), and its layout lists them, before any store is made.
handler.declare(transcripts().flatMap(([name]) => transcript(name).declarations ?? []));
const layout = handler.layout();
const behaviors = new Map(layout.behaviors.map(behavior => [behavior.name, behavior]));
const useId = (entry: Use): string => (entry.anchor === 'type' ? entry.behavior : entry.anchor.field);

function parsed(answer: unknown): Record<string, unknown> {
  return (answer instanceof RawJson ? JSON.parse(answer.text) : answer) as Record<string, unknown>;
}

function readRow(store: SqliteStore, type: string, id: string): { id: string; version: number; data: JsonObject; owned: Record<string, JsonObject> } | null {
  return parsed(store.read({ kind: 'row', type, id })).row as never;
}

/** Owned fields with others set on them, a null clearing one, and a use left with none dropped: the bridge's set_owned. */
function setOwned(held: Record<string, JsonObject>, owned: Record<string, JsonObject>): Record<string, JsonObject> {
  const out: Record<string, JsonObject> = structuredClone(held);
  for (const [use, fields] of Object.entries(owned)) {
    const slot = (out[use] ??= {});
    for (const [field, value] of Object.entries(fields)) {
      if (value === null) delete slot[field];
      else slot[field] = value;
    }
  }
  return Object.fromEntries(
    Object.entries(out)
      .filter(([, fields]) => Object.keys(fields).length > 0)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
  );
}

interface Counts {
  commands: number;
  checks: number;
  raws: number;
}

class Replay {
  readonly stores = new Map<string, SqliteStore>();
  readonly live = new Map<string, number>();
  readonly events = new Map<string, number>();
  readonly mismatches: string[] = [];

  constructor(
    readonly scenario: Scenario,
    readonly make: () => SqliteStore,
  ) {}

  store(database: string): SqliteStore {
    let store = this.stores.get(database);
    if (store === undefined) {
      store = this.make();
      this.stores.set(database, store);
    }
    return store;
  }

  compare(step: number, call: string, what: string, expected: unknown, actual: unknown): void {
    if (!same(expected, actual)) {
      this.mismatches.push(`step ${step} (${call}) ${what}:\n    expected ${JSON.stringify(expected)}\n    actual   ${JSON.stringify(actual)}`);
    }
  }

  types(): { name: string; versions?: Spec[] }[] {
    return this.scenario.types ?? [];
  }

  /** Every type of the scenario at the version it last made live, the command's own at own. */
  specs(own: Spec): Record<string, Spec> {
    const specs: Record<string, Spec> = {};
    for (const type of this.types()) {
      const spec = type.versions?.[this.live.get(type.name) ?? 0];
      if (spec !== undefined) specs[type.name] = spec;
    }
    specs[own.name] = own;
    return specs;
  }

  replay(counts: Counts): void {
    for (const step of this.scenario.steps ?? []) {
      if (step.kind === 'check') {
        counts.checks++;
        this.check(step);
      } else if (step.kind === 'raw') {
        counts.raws++;
        this.raw(step);
      } else {
        counts.commands++;
        this.command(step);
      }
    }
  }

  check(step: Extract<Step, { kind: 'check' }>): void {
    const { spec, command, expect } = step;
    if (expect.refused === undefined) {
      const index = this.types()
        .find(known => known.name === spec.name)
        ?.versions?.findIndex(version => same(version, spec));
      if (index !== undefined && index >= 0) this.live.set(spec.name, index);
    }
    const store = this.store(step.database);
    const reads: unknown[] = [];
    let refused: unknown = null;
    for (let round = 0; round < 16; round++) {
      const ran = JSON.parse(handler.call('core', JSON.stringify({ spec, command, answers: { reads } }))) as {
        done?: { refused?: unknown };
        read?: { kind: string; type: string }[];
      };
      if (ran.done !== undefined) {
        refused = ran.done.refused ?? null;
        break;
      }
      if (ran.read === undefined) throw new Error(`step ${step.step}: a check asked for ${JSON.stringify(ran)}`);
      for (const request of ran.read) {
        if (request.kind !== 'hasRows') throw new Error(`step ${step.step}: a check asked for ${JSON.stringify(request)}`);
        const rows = parsed(store.read({ kind: 'rows', type: request.type })).rows as unknown[];
        reads.push({ request, answer: { hasRows: rows.length > 0 } });
      }
    }
    if (refused === null && command.action.kind === 'publish') {
      this.dropSharedOfRemovedUses(step.database, spec);
    }
    this.compare(step.step, step.call, 'refused', expect.refused ?? null, refused);
  }

  /** Drops the shared tables' rows of each use of a type that a published version no longer lists, as development mode's publish does. */
  dropSharedOfRemovedUses(database: string, spec: Spec): void {
    const writes: StoreWrite[] = [];
    const versions = this.types().find(known => known.name === spec.name)?.versions ?? [];
    for (const entry of versions.flatMap(version => version.uses ?? [])) {
      const kept = (spec.uses ?? []).some(used => useId(used) === useId(entry) && used.behavior === entry.behavior);
      const shares = behaviors.get(entry.behavior)?.tables.some(table => table.shared) ?? false;
      const write: StoreWrite = { write: 'dropUse', type: spec.name, behavior: entry.behavior, useId: useId(entry) };
      if (!kept && shares && !writes.some(known => same(known, write))) writes.push(write);
    }
    if (writes.length === 0) return;
    const store = this.store(database);
    store.begin();
    const applied = store.apply({ writes });
    store.commit();
    if (!applied) throw new Error("a drop of a removed use's shared rows moved");
  }

  raw(step: Extract<Step, { kind: 'raw' }>): void {
    const store = this.store(step.database);
    const row = readRow(store, step.type, step.key);
    if (row === null) {
      this.mismatches.push(`step ${step.step} (${step.call}): no row ${step.type} ${step.key} to write`);
      return;
    }
    const unit: Unit = {
      writes: [{ write: 'update', type: step.type, id: step.key, expected: row.version, data: row.data, owned: setOwned(row.owned, step.owned) }],
    };
    store.begin();
    const applied = store.apply(unit);
    store.commit();
    this.compare(step.step, step.call, 'raw.applied', true, applied);
  }

  /** Runs a command through the bridge in a transaction of its own, again from fresh state when a fence did not hold. */
  execute(specs: Record<string, Spec>, spec: Spec, command: Command, store: SqliteStore): { refused: unknown } | { changes: { result?: Json; rows?: { type: string; key?: string }[] } } {
    const head = `{"specs":${JSON.stringify(specs)},"spec":${JSON.stringify(spec)},"command":${JSON.stringify(command)}`;
    for (let attempt = 0; attempt < 8; attempt++) {
      store.begin();
      const answers: string[] = [];
      for (;;) {
        const ran = JSON.parse(handler.call('bridge', `${head},"answers":{"reads":[${answers.join(',')}]}}`)) as {
          read?: unknown[];
          refused?: unknown;
          changed?: { changes: { result?: Json; rows?: { type: string; key?: string }[] }; writes: StoreWrite[] };
        };
        if (ran.read !== undefined) {
          for (const read of ran.read) {
            answers.push(`[${JSON.stringify(read)},${(store.read(read as never) as RawJson).text}]`);
          }
          continue;
        }
        if (ran.refused !== undefined) {
          store.rollback();
          return { refused: ran.refused };
        }
        const changed = ran.changed!;
        if (store.apply({ writes: changed.writes })) {
          store.commit();
          return { changes: changed.changes };
        }
        store.rollback();
        break;
      }
    }
    return { refused: { code: 'internal', status: 500, class: 'Error', message: 'the command kept moving' } };
  }

  command(step: Extract<Step, { kind: 'command' }>): void {
    const { command, expect } = step;
    const spec = this.types().find(known => known.name === step.type)?.versions?.[step.version];
    if (spec === undefined) {
      this.mismatches.push(`step ${step.step} (${step.call}): no version ${step.version} of ${step.type}`);
      return;
    }
    this.live.set(step.type, step.version);
    const store = this.store(step.database);
    const outcome = this.execute(this.specs(spec), spec, command, store);
    const refused = 'refused' in outcome ? outcome.refused : null;
    const result = 'changes' in outcome ? (outcome.changes.result ?? null) : null;
    const written = 'changes' in outcome ? (outcome.changes.rows ?? []) : [];
    this.compare(step.step, step.call, 'refused', expect.refused ?? null, refused);
    this.compare(step.step, step.call, 'result', expect.result ?? null, result);
    const action = command.action;
    const rowKey = action.kind === 'create' ? (action.id ?? null) : (action.key ?? null);
    if (expect.key !== undefined) {
      this.compare(step.step, step.call, 'key', expect.key, rowKey);
    }
    if (rowKey !== null && rowKey !== '') {
      this.compareRow(step, spec, rowKey);
    }
    const others: [string, string][] = [];
    for (const write of written) {
      if (write.key === undefined || write.key === null) continue;
      const mine = write.type === step.type && rowKey === write.key;
      if (!mine && !others.some(([type, key]) => type === write.type && key === write.key)) {
        others.push([write.type, write.key]);
      }
    }
    const actual = others.map(([type, key]) => {
      const row = readRow(store, type, key);
      return row === null ? { type, key } : { type, key, row: { own: row.data, owned: row.owned } };
    });
    this.compare(step.step, step.call, 'others', expect.others ?? null, actual.length === 0 ? null : actual);
    const after = this.events.get(step.database) ?? 0;
    const events = store.events(after);
    if (events.length > 0) this.events.set(step.database, events[events.length - 1]!.seq);
    this.compare(
      step.step,
      step.call,
      'events',
      expect.events ?? [],
      events.map(event => ({ type: event.type, key: event.id, useId: event.useId, kind: event.kind, at: event.at, payload: parsed(event.payload) })),
    );
  }

  compareRow(step: Extract<Step, { kind: 'command' }>, spec: Spec, rowKey: string): void {
    const store = this.store(step.database);
    const row = readRow(store, step.type, rowKey);
    this.compare(step.step, step.call, 'row', step.expect.row ?? null, row === null ? null : { own: row.data, owned: row.owned });
    if (step.expect.version !== undefined) {
      this.compare(step.step, step.call, 'version', step.expect.version, row?.version ?? null);
    }
    // The sub-table rows of each use of any of the type's versions.
    const uses: [string, string][] = [];
    const versions = this.types().find(known => known.name === step.type)?.versions ?? [spec];
    for (const version of versions) {
      for (const entry of version.uses ?? []) {
        if (!uses.some(([id, behavior]) => id === useId(entry) && behavior === entry.behavior)) uses.push([useId(entry), entry.behavior]);
      }
    }
    const subRows: Record<string, Record<string, unknown>> = {};
    for (const [id, behavior] of uses) {
      const declared = behaviors.get(behavior);
      if (declared === undefined) continue;
      const tables: Record<string, unknown> = {};
      // A shared table is the type's as a whole, no row's.
      for (const table of declared.tables.filter(table => !table.shared)) {
        const rows = parsed(store.read({ kind: 'subRows', type: step.type, id: rowKey, useId: id, table: table.name })).subRows as unknown[];
        if (rows.length > 0) tables[table.name] = rows;
      }
      if (Object.keys(tables).length > 0) subRows[id] = tables;
    }
    this.compare(step.step, step.call, 'subRows', step.expect.subRows ?? null, Object.keys(subRows).length === 0 ? null : subRows);
    const armed = store.armed(step.type, rowKey);
    this.compare(step.step, step.call, 'deadlines', step.expect.deadlines ?? null, armed.length === 0 ? null : armed);
  }
}

/** Replays every scenario of every transcript named over stores make makes, failing on any mismatch or on fewer steps than a floor. */
function replayAll(over: string, names: readonly string[], make: () => SqliteStore): void {
  const listed = transcripts();
  expect(listed.length).toBeGreaterThanOrEqual(27);
  for (const [name, floor] of listed.filter(([name]) => names.length === 0 || names.includes(name))) {
    const recorded = transcript(name);
    const counts: Counts = { commands: 0, checks: 0, raws: 0 };
    const failed: string[] = [];
    for (const scenario of recorded.scenarios) {
      const replay = new Replay(scenario, make);
      replay.replay(counts);
      if (replay.mismatches.length > 0) failed.push(`${scenario.name}:\n  ${replay.mismatches.join('\n  ')}`);
    }
    const replayed = counts.commands + counts.checks + counts.raws;
    const steps = recorded.scenarios.reduce((sum, scenario) => sum + (scenario.steps?.length ?? 0), 0);
    console.log(`${name} over ${over}: ${recorded.scenarios.length} scenarios, ${replayed} steps (${counts.commands} commands, ${counts.checks} checks, ${counts.raws} raw writes)`);
    expect({ name, failed }).toEqual({ name, failed: [] });
    expect(replayed).toBeGreaterThanOrEqual(floor);
    expect(replayed).toBe(steps);
  }
}

test('the transcripts replay over bun:sqlite in memory', () => {
  replayAll('bun:sqlite in memory', [], () => sqlite());
}, 600_000);

const files = scratch('transcripts');
afterAll(files.remove);

test('the transcripts replay over a bun:sqlite file', () => {
  let n = 0;
  replayAll('a bun:sqlite file', ['core-workflow', 'core-timers', 'core-approvals', 'core-schedules', 'core-pool'], () => sqlite(join(files.dir, `s${++n}.db`)));
}, 600_000);
