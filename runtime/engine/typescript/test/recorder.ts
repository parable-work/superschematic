// The scenario recorder (D58): with ENGINE_RECORD=<dir> set, every engine a
// test opens through helpers.ts is a proxy that records each public call
// the test makes as a step, and every app a test serves through
// recordedApp records its exchanges, so a second implementation of the
// engine, its bindings and the servers can replay what this one did. Each
// test is one scenario, named by its full name; at exit each test file's
// scenarios are written to <dir>/scenarios/<file>.json, or to
// <dir>/http/<file>.json when its tests served HTTP.
// runtime/engine/testdata/scenarios/README.md and
// runtime/engine/testdata/http/README.md give the format; the corpus test
// (corpus.test.ts) holds the committed files to what this writes.
//
// Recording keeps what the engine does but for two sources of chance, so
// recording twice writes the same files: crypto.randomUUID, which the
// version graph and the HTTP runtime draw from, and the ids of an engine
// opened without its own both come from a generator seeded per test
// (seededUUID), and an engine opened without a clock reads Date.now, but
// never the same millisecond twice. A policy answers as the test's, wrapped
// only to see what passes through.
import { AsyncLocalStorage } from 'node:async_hooks';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join, relative, resolve } from 'node:path';

import { allowAll, permissionPolicy, type Engine, type EngineOptions } from '../dist/index.js';

/** The directory the recorder writes to; undefined when it is off. */
export const RECORD_DIR: string | undefined = process.env.ENGINE_RECORD ? resolve(process.env.ENGINE_RECORD) : undefined;

/** Whether this run records. */
export const recording = RECORD_DIR !== undefined;

type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };
type JSONObject = { [key: string]: JSONValue };

/** What a test context gives the recorder: node:test's, which Bun's twin matches. */
export interface TestContextLike {
  readonly fullName?: string;
  readonly name?: string;
  readonly filePath?: string;
}

// One recorded test: its engines, principals, steps and what makes it
// replayable by something other than this engine.
interface Case {
  readonly steps: JSONObject[];
  readonly engines: EngineRecord[];
  readonly principals: Map<string, { name: string; value: JSONValue }>;
  readonly subjects: Map<string, number>;
  readonly js: Set<string>;
  readonly testBehaviors: Set<string>;
  readonly registered: Set<string>;
  readonly composed: Set<string>;
  // What the clocks read while the case ran: the wall clock, whose span
  // alone symbolize writes as {{time:N}}, and the test's fixed clocks,
  // whose times it never does.
  readonly wallReadings: number[];
  readonly fixedReadings: number[];
  fixedClock: boolean;
  fixedIds: boolean;
  http: boolean;
  streams: number;
  apps: number;
}

interface EngineRecord {
  readonly label: string;
  // The case it was opened in: a call on it in another test is not recorded.
  readonly case: Case;
  readonly open: JSONObject;
  // The policy's answers by question, in the order first asked, when the
  // test passed a policy of its own; undefined for allowAll and
  // permissionPolicy.
  readonly answers?: Map<string, { request: JSONValue; answer: JSONValue }>;
  // The core's behaviors, which every engine registers.
  readonly core: Set<string>;
  // The value the fixed clock last read in a step, or undefined.
  now?: number;
}

// The step a call is running in, which a fixed clock and the id generator
// report to: a call's, or an exchange's across its awaits.
interface Active {
  readonly step: JSONObject;
  readonly engine: EngineRecord | undefined;
  // What a fixed clock read during the step.
  readonly readings: number[];
  readonly ids: string[];
}

const exchanges = new AsyncLocalStorage<Active>();

let current: Case | undefined;
let active: Active | undefined;
let depth = 0;
const files = new Map<string, { source: string; cases: Array<{ name: string; case: Case }> }>();
// What generated values look like, to be written as symbols: every id the
// engine's default generator and crypto.randomUUID made, hyphenated and in
// the version graph's base62 form, and the temporary directories.
const generated = new Set<string>();
const generated62 = new Map<string, string>();
const directories: string[] = [];
const raws = new WeakMap<object, Engine>();

// The ids drawn in the current test, and the wall clock's last reading.
let drawn = 0;
let lastWall = 0;

if (recording) {
  globalThis.crypto.randomUUID = seededUUID as typeof globalThis.crypto.randomUUID;
  process.on('exit', writeFiles);
}

/**
 * seededUUID is the recorder's random UUID: the n-th id a test draws, from
 * 1, is the version-4 UUID made of the first 16 bytes of
 * SHA-256("superschematic-engine-scenario/<n>"), with the version and
 * variant bits set. A replayer that draws its ids in the same order from
 * the same generator gets the same ids, and so the same content hashes.
 */
export function seededUUID(): string {
  drawn += 1;
  const bytes = createHash('sha256').update(`superschematic-engine-scenario/${drawn}`).digest().subarray(0, 16);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString('hex');
  const id = `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`;
  generated.add(id);
  generated62.set(base62(id), id);
  return id;
}

// wallClock is Date.now, but past the last reading: the clock of an engine
// opened without one, whose readings the corpus writes as symbols, so each
// one is a symbol of its own.
function wallClock(): number {
  lastWall = Math.max(Date.now(), lastWall + 1);
  return lastWall;
}

// base62 is a hyphenated UUID's canonical form in the version graph:
// base62 over 0-9A-Za-z, no padding.
function base62(id: string): string {
  const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
  let n = BigInt(`0x${id.replace(/-/g, '')}`);
  if (n === 0n) {
    return '0';
  }
  let out = '';
  while (n > 0n) {
    out = alphabet[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out;
}

/** registerDirectory names a temporary directory the recorder writes as {{dir:N}}. */
export function registerDirectory(directory: string): void {
  if (recording) {
    directories.push(directory);
  }
}

function caseNow(): Case {
  current ??= {
    steps: [],
    engines: [],
    principals: new Map(),
    subjects: new Map(),
    js: new Set(),
    testBehaviors: new Set(),
    registered: new Set(),
    composed: new Set(),
    wallReadings: [],
    fixedReadings: [],
    fixedClock: false,
    fixedIds: false,
    http: false,
    streams: 0,
    apps: 0,
  };
  return current;
}

/**
 * testHooks declares hooks: state a test behavior reads that the running
 * test sets, such as runner-fixtures.ts's probe, whose closures
 * test.Ledger's reactions and schedule run, or a value a guard compares a
 * request with. What hooks holds when it is declared is what the behavior
 * does with nothing set, and so what another implementation of the
 * behavior does, since a recording carries nothing the test sets. When a
 * behavior reads a hook whose value is not its declared one (a closure the
 * test put there, a value it changed), what the engine does depends on
 * code or data the corpus does not hold: the recorder marks the case `js`
 * (`testHook.<name>.<member>`) and the step the read came in. Without
 * ENGINE_RECORD it returns hooks.
 */
export function testHooks<T extends object>(name: string, hooks: T): T {
  if (!recording) {
    return hooks;
  }
  const declared = new Map<string, unknown>(Object.entries(hooks).map(([member, value]) => [member, hookValue(value)]));
  return new Proxy(hooks, {
    get(target, property, receiver) {
      const value = Reflect.get(target, property, receiver);
      if (typeof property === 'string' && hookValue(value) !== declared.get(property)) {
        noteHook(`testHook.${name}.${property}`);
      }
      return value;
    },
  });
}

// hookValue is what a hook's value is compared by: a function by identity,
// anything else by its JSON.
function hookValue(value: unknown): unknown {
  return typeof value === 'function' || value === undefined ? value : JSON.stringify(value);
}

// noteHook marks the case, and the step a behavior read the hook in, as
// depending on what the test set. A read outside any step (a started
// runner's) marks the case alone.
function noteHook(reason: string): void {
  const record = current;
  if (record === undefined) {
    return;
  }
  record.js.add(reason);
  const where = active ?? exchanges.getStore();
  if (where !== undefined) {
    where.step.js = true;
  }
}

/** rawEngine is the engine a recording proxy stands for, or the engine itself. */
export function rawEngine(engine: Engine): Engine {
  return raws.get(engine) ?? engine;
}

/**
 * recordOpen opens an engine with open, recording the open as a step, and
 * returns a proxy that records the calls made through it. Without
 * ENGINE_RECORD it is open(options).
 */
export function recordOpen(options: EngineOptions, open: (options: EngineOptions) => Engine): Engine {
  if (!recording) {
    return open(options);
  }
  const record = caseNow();
  const label = `e${record.engines.length + 1}`;
  const opened: JSONObject = { call: 'open', as: label };
  const wrapped: EngineOptions = { ...options };
  const described: JSONObject = {};
  const engine: EngineRecord = {
    label,
    case: record,
    open: opened,
    core: new Set(),
    ...(policyKind(options.policy) === 'custom' ? { answers: new Map() } : {}),
  };
  record.engines.push(engine);

  for (const [key, value] of Object.entries(options)) {
    if (value === undefined || key === 'driver') {
      continue;
    }
    switch (key) {
      case 'path':
        described.path = String(value);
        break;
      case 'policy': {
        const kind = policyKind(value);
        described.policy = kind === 'custom' ? { answers: [] } : kind;
        if (kind === 'custom') {
          const policy = value as EngineOptions['policy'];
          wrapped.policy = (request) => {
            const answer = policy(request);
            notePolicy(engine, request, answer);
            return answer;
          };
        }
        break;
      }
      case 'metaSchema':
        described.metaSchema = metaSchemaKind(value);
        break;
      case 'behaviors':
        described.behaviors = (value as EngineOptions['behaviors'] ?? []).map((implementation) => behaviorName(implementation));
        for (const name of described.behaviors as string[]) {
          record.testBehaviors.add(name);
        }
        break;
      case 'ids': {
        described.ids = 'fixed';
        record.fixedIds = true;
        const ids = value as () => string;
        wrapped.ids = () => {
          const id = ids();
          noteId(id);
          return id;
        };
        break;
      }
      case 'clock': {
        described.clock = 'fixed';
        record.fixedClock = true;
        const clock = value as () => number;
        wrapped.clock = () => {
          const now = clock();
          noteReading(now, 'fixed');
          return now;
        };
        break;
      }
      case 'permissionMatcher':
        described.permissionMatcher = 'custom';
        record.js.add('permissionMatcher');
        break;
      case 'values': {
        const values = encode(value, record, 'options');
        if (isObject(values) && values.driver !== undefined) {
          values.driver = 'custom';
          record.js.add('values.driver');
        }
        described.values = values;
        break;
      }
      default:
        described[key] = encode(value, record, 'options');
    }
  }
  if (options.ids === undefined) {
    described.ids = 'seeded';
    wrapped.ids = seededUUID;
  }
  if (options.clock === undefined) {
    described.clock = 'wall';
    wrapped.clock = () => {
      const now = wallClock();
      noteReading(now, 'wall');
      return now;
    };
  }
  opened.args = [described];

  const raw = run(engine, opened, () => open(wrapped));
  for (const name of raw.behaviors.names()) {
    engine.core.add(name);
  }
  for (const name of record.testBehaviors) {
    engine.core.delete(name);
  }
  const proxy = engineProxy(raw, engine);
  raws.set(proxy, raw);
  engineLabels.set(proxy, label);
  return proxy;
}

function policyKind(policy: unknown): 'allowAll' | 'permission' | 'custom' {
  if (policy === allowAll) {
    return 'allowAll';
  }
  if (typeof policy === 'function' && policy.toString() === permissionPolicy().toString()) {
    return 'permission';
  }
  return 'custom';
}

// The core meta-schema with behaviors let through (behavior-fixtures.ts's
// openMetaSchema), which the corpus names "open" rather than holding.
let openMetaSchemaText: string | undefined;

function metaSchemaKind(value: unknown): JSONValue {
  if (openMetaSchemaText === undefined) {
    const metaSchema = JSON.parse(readFileSync(new URL('../../../../ir/typescript/schema-file.json', import.meta.url), 'utf8')) as {
      $defs: { TypeDef: { properties: { behaviors: Record<string, unknown> } }; BehaviorRef: { allOf?: unknown; properties: { name: Record<string, unknown> } } };
    };
    delete metaSchema.$defs.TypeDef.properties.behaviors.maxItems;
    delete metaSchema.$defs.BehaviorRef.properties.name.enum;
    delete metaSchema.$defs.BehaviorRef.allOf;
    openMetaSchemaText = JSON.stringify(metaSchema);
  }
  const text = typeof value === 'string' ? JSON.stringify(JSON.parse(value)) : JSON.stringify(value);
  return text === openMetaSchemaText ? 'open' : (JSON.parse(text) as JSONValue);
}

function behaviorName(implementation: unknown): string {
  const name = (implementation as { declaration?: { name?: unknown } } | undefined)?.declaration?.name;
  return typeof name === 'string' ? name : '?';
}

function notePolicy(engine: EngineRecord, request: unknown, answer: unknown): void {
  const record = current;
  if (record === undefined || engine.answers === undefined) {
    return;
  }
  const asked = encodeRequest(request, record);
  const key = JSON.stringify(asked);
  const given = typeof answer === 'boolean' ? answer : encode(answer, record, 'policy');
  if (typeof answer !== 'boolean') {
    record.js.add('policy');
  }
  const seen = engine.answers.get(key);
  if (seen === undefined) {
    engine.answers.set(key, { request: asked, answer: given });
  } else if (JSON.stringify(seen.answer) !== JSON.stringify(given)) {
    // A policy whose answer to one question changes is not a table.
    record.js.add('policy');
  }
}

function encodeRequest(request: unknown, record: Case): JSONValue {
  if (!isObject(request)) {
    return encode(request, record, 'policy');
  }
  const out: JSONObject = {};
  for (const [key, value] of Object.entries(request)) {
    if (value === undefined) {
      continue;
    }
    out[key] = key === 'principal' ? principalOf(value, record) : encode(value, record, 'policy');
  }
  return out;
}

// noteReading notes what a clock read. A fixed clock's reading goes on the
// step it was read in, which settle writes as its `now` or `clock`, and on
// the case, whose fixed readings endCase holds apart from the wall clock's;
// the wall clock's goes on the case alone, whose wall readings make the
// span symbolize writes times in.
function noteReading(now: number, clock: 'wall' | 'fixed'): void {
  if (clock === 'wall') {
    current?.wallReadings.push(now);
    return;
  }
  const where = active ?? exchanges.getStore();
  if (where !== undefined) {
    where.readings.push(now);
  }
  current?.fixedReadings.push(now);
}

function noteId(id: string): void {
  const where = active ?? exchanges.getStore();
  if (where !== undefined) {
    where.ids.push(id);
  }
}

// The calls the engine's components take a principal first in.
const PRINCIPAL_FIRST = new Set([
  'namespaces.list',
  'namespaces.get',
  'namespaces.create',
  'namespaces.archive',
  'namespaces.unarchive',
  'schemas.define',
  'schemas.publish',
  'schemas.live',
  'schemas.draft',
  'schemas.version',
  'schemas.list',
  'schemas.capabilities',
  'schemas.validate',
  'schemas.validator',
  'schemas.behaviors',
  'instances.create',
  'instances.get',
  'instances.list',
  'instances.lookup',
  'instances.update',
  'instances.delete',
  'instances.invoke',
  'instances.operate',
  'instances.invokeSchema',
  'events.read',
  'values.get',
  'tools.listBehaviors',
  'tools.describeBehavior',
  'tools.describe',
  'tools.manifest',
  'tools.call',
  'search',
]);

const COMPONENTS = new Set(['namespaces', 'schemas', 'instances', 'events', 'tools', 'values', 'runner', 'behaviors', 'storage']);
// What a test may call on storage and the behavior registry that the
// corpus records; the rest passes through unrecorded.
const STORAGE_CALLS = new Set(['exec', 'run', 'get', 'all', 'transaction']);
const BEHAVIOR_CALLS = new Set(['register', 'has', 'names', 'declaration']);

function engineProxy(raw: Engine, engine: EngineRecord): Engine {
  const components = new Map<string, object>();
  return new Proxy(raw, {
    get(target, property, receiver) {
      const value = Reflect.get(target, property, receiver === undefined ? target : target);
      if (typeof property !== 'string') {
        return value;
      }
      if (COMPONENTS.has(property) && typeof value === 'object' && value !== null) {
        let proxy = components.get(property);
        if (proxy === undefined) {
          proxy = componentProxy(value, property, engine);
          components.set(property, proxy);
        }
        return proxy;
      }
      if ((property === 'search' || property === 'close') && typeof value === 'function') {
        return (...args: unknown[]) => recordCall(engine, property, args, () => (value as (...a: unknown[]) => unknown).apply(target, args));
      }
      return typeof value === 'function' ? value.bind(target) : value;
    },
    set(target, property, value) {
      return Reflect.set(target, property, value);
    },
  });
}

function componentProxy(component: object, name: string, engine: EngineRecord): object {
  return new Proxy(component, {
    // A test that replaces a method (to count an app's reads, say) puts it
    // on the component itself, where the app calls it too: what it calls
    // through the proxy from there is the app's call, not a step.
    set(target, property, value) {
      if (typeof value !== 'function') {
        return Reflect.set(target, property, value);
      }
      const replaced = value as (...args: unknown[]) => unknown;
      return Reflect.set(target, property, function (this: unknown, ...args: unknown[]) {
        depth += 1;
        try {
          return replaced.apply(this, args);
        } finally {
          depth -= 1;
        }
      });
    },
    get(target, property) {
      const value = Reflect.get(target, property, target);
      if (typeof property !== 'string' || typeof value !== 'function') {
        return value;
      }
      if ((name === 'storage' && !STORAGE_CALLS.has(property)) || (name === 'behaviors' && !BEHAVIOR_CALLS.has(property))) {
        return value.bind(target);
      }
      return (...args: unknown[]) => recordCall(engine, `${name}.${property}`, args, () => (value as (...a: unknown[]) => unknown).apply(target, args));
    },
  });
}

// recordCall runs a call as a step, unless it runs inside another step (a
// test's own wrapper around a method, a storage transaction) or inside an
// exchange, whose app is the one calling.
function recordCall(engine: EngineRecord, call: string, args: unknown[], invoke: () => unknown): unknown {
  if (depth > 0 || exchanges.getStore() !== undefined || current !== engine.case) {
    return invoke();
  }
  const record = engine.case;
  const step: JSONObject = { engine: engine.label, call };
  let rest = args;
  if (PRINCIPAL_FIRST.has(call)) {
    step.principal = principalOf(args[0], record);
    rest = args.slice(1);
  }
  while (rest.length > 0 && rest[rest.length - 1] === undefined) {
    rest = rest.slice(0, -1);
  }
  if (call === 'behaviors.register') {
    const name = behaviorName(args[0]);
    step.args = [{ behavior: name }];
    record.testBehaviors.add(name);
    record.registered.add(name);
  } else if (rest.length > 0) {
    step.args = rest.map((arg) => encode(arg, record, 'args', step));
  }
  if (call === 'storage.transaction') {
    record.js.add('storage.transaction');
  }
  if (call === 'events.watch') {
    record.js.add('events.watch');
  }
  if (call.startsWith('storage.')) {
    step.dialect = 'sqlite';
  }
  if (call === 'schemas.define') {
    noteComposed(args[1], record);
  }
  return run(engine, step, invoke);
}

// run runs invoke as the step's call, recording its result or its error,
// and a fixed clock's readings and the ids drawn while it ran.
function run<T>(engine: EngineRecord, step: JSONObject, invoke: () => T): T {
  const record = caseNow();
  const previous = active;
  const here: Active = { step, engine, readings: [], ids: [] };
  active = here;
  depth += 1;
  try {
    const result = invoke();
    if (result !== undefined && step.call !== 'open') {
      step.expect = encode(result, record, 'result', step);
    }
    return result;
  } catch (error) {
    step.error = encodeError(error, record);
    throw error;
  } finally {
    depth -= 1;
    active = previous;
    settle(record, here);
    record.steps.push(step);
  }
}

// settle writes what the fixed clock read and the fixed ids drawn during a
// step onto it: `now` when the clock read one value, which differs from
// the last one written for the engine, `clock` when it read several, and
// `ids` for the ids the generator gave.
function settle(record: Case, here: Active): void {
  const engine = here.engine;
  if (record.fixedClock && engine !== undefined && here.readings.length > 0 && engineHasFixedClock(engine)) {
    const distinct = [...new Set(here.readings)];
    if (distinct.length === 1) {
      if (engine.now !== distinct[0]) {
        here.step.now = distinct[0];
        engine.now = distinct[0];
      }
    } else {
      here.step.clock = here.readings;
      engine.now = here.readings[here.readings.length - 1];
    }
  }
  if (record.fixedIds && here.ids.length > 0) {
    here.step.ids = here.ids;
  }
}

function engineHasFixedClock(engine: EngineRecord): boolean {
  const options = (engine.open.args as JSONObject[] | undefined)?.[0];
  return options?.clock === 'fixed';
}

function noteComposed(document: unknown, record: Case): void {
  let parsed = document;
  if (typeof document === 'string') {
    try {
      parsed = JSON.parse(document);
    } catch {
      return;
    }
  }
  const types = isObject(parsed) ? parsed.types : undefined;
  if (!isObject(types)) {
    return;
  }
  for (const type of Object.values(types)) {
    const behaviors = isObject(type) ? type.behaviors : undefined;
    if (Array.isArray(behaviors)) {
      for (const behavior of behaviors) {
        if (isObject(behavior) && typeof behavior.name === 'string') {
          record.composed.add(behavior.name);
        }
      }
    }
  }
}

// principalOf names a principal in the case's table: its subject, then
// `subject#2` and on for another principal with the same subject. A value
// that is not an object with a subject is written {"$given": value}.
function principalOf(value: unknown, record: Case): JSONValue {
  if (!isObject(value) || typeof value.subject !== 'string') {
    return { $given: encode(value, record, 'args') };
  }
  const encoded = encode(value, record, 'args');
  const key = JSON.stringify(encoded);
  const known = record.principals.get(key);
  if (known !== undefined) {
    return known.name;
  }
  const subject = value.subject;
  const count = (record.subjects.get(subject) ?? 0) + 1;
  record.subjects.set(subject, count);
  const name = count === 1 ? subject : `${subject}#${count}`;
  record.principals.set(key, { name, value: encoded });
  return name;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// encode writes a value as JSON. What JSON cannot hold is written as an
// object with one `$` member, and marks the step (`js`) and the case as
// replayable only by this engine: `$undefined` (an absent argument before
// a given one, or an array element), `$number` (NaN, Infinity, -Infinity),
// `$bigint`, `$function`, `$object` (an instance of a class, by name),
// `$date`, `$bytes` (hex) and `$cycle`.
function encode(value: unknown, record: Case, where: string, step?: JSONObject, seen: Set<object> = new Set()): JSONValue {
  const mark = (reason: string): void => {
    record.js.add(reason);
    if (step !== undefined) {
      step.js = true;
    }
  };
  switch (typeof value) {
    case 'string':
    case 'boolean':
      return value;
    case 'number':
      if (Number.isFinite(value)) {
        return value;
      }
      mark(where);
      return { $number: String(value) };
    case 'bigint':
      mark(where);
      return { $bigint: value.toString() };
    case 'undefined':
      mark(where);
      return { $undefined: true };
    case 'function':
      mark(where);
      return { $function: value.name || 'anonymous' };
    case 'symbol':
      mark(where);
      return { $object: 'Symbol' };
  }
  if (value === null) {
    return null;
  }
  const object = value as object;
  if (seen.has(object)) {
    mark(where);
    return { $cycle: true };
  }
  if (object instanceof Uint8Array) {
    // A BLOB a storage read returns, as SQLite holds it.
    return { $bytes: Buffer.from(object.buffer, object.byteOffset, object.byteLength).toString('hex') };
  }
  if (object instanceof Date) {
    mark(where);
    return { $date: Number.isNaN(object.getTime()) ? 'Invalid Date' : object.toISOString() };
  }
  if (typeof (object as { then?: unknown }).then === 'function') {
    mark(where);
    return { $object: 'Promise' };
  }
  seen.add(object);
  try {
    if (Array.isArray(object)) {
      return object.map((item) => encode(item, record, where, step, seen));
    }
    const prototype = Object.getPrototypeOf(object);
    if (prototype !== Object.prototype && prototype !== null) {
      // An instance of a class, an error among them: its class's name.
      mark(where);
      return { $object: object instanceof Error ? object.name : ((prototype.constructor?.name as string | undefined) ?? 'Object') };
    }
    const out: JSONObject = {};
    for (const [key, member] of Object.entries(object)) {
      if (member === undefined) {
        continue;
      }
      out[key] = encode(member, record, where, step, seen);
    }
    return out;
  } finally {
    seen.delete(object);
  }
}

// encodeError writes a thrown error as its class, its code, its message
// and its other own members (details).
function encodeError(error: unknown, record: Case): JSONObject {
  if (!(error instanceof Error)) {
    return { class: 'thrown', message: String(error), details: { value: encode(error, record, 'error') } };
  }
  const out: JSONObject = { class: error.name };
  const code = (error as { code?: unknown }).code;
  if (code !== undefined) {
    out.code = encode(code, record, 'error');
  }
  out.message = error.message;
  const details: JSONObject = {};
  for (const [key, value] of Object.entries(error)) {
    if (key === 'name' || key === 'code' || key === 'message' || key === 'stack' || key === 'cause' || value === undefined) {
      continue;
    }
    details[key] = encode(value, record, 'error');
  }
  if (Object.keys(details).length > 0) {
    out.details = details;
  }
  return out;
}

// HTTP: an app the test serves through recordedApp records each exchange.

/** The request headers an exchange records; the rest are the transport's. */
const REQUEST_HEADERS = [
  'accept',
  'authorization',
  'content-type',
  'if-match',
  'last-event-id',
  'mcp-protocol-version',
  'mcp-session-id',
  'origin',
  'preconditions',
  'service-authorization',
  'x-request-id',
];
/** The response headers an exchange records. */
const RESPONSE_HEADERS = ['allow', 'cache-control', 'content-type', 'etag', 'location', 'mcp-session-id', 'retry-after', 'www-authenticate', 'x-request-id'];

interface Fetching {
  fetch(request: Request, ...rest: unknown[]): Response | Promise<Response>;
}

/**
 * recordedCallers wraps the app options' authenticate and
 * authenticateService so each exchange records the caller the host
 * established: the end user's principal and the verified service. Without
 * ENGINE_RECORD it returns the options.
 */
export function recordedCallers<O extends object>(options: O): O {
  if (!recording) {
    return options;
  }
  const wrapped = { ...options } as Record<string, unknown>;
  const authenticate = wrapped.authenticate as ((ctx: unknown) => unknown) | undefined;
  if (typeof authenticate === 'function') {
    const recorded = async (ctx: unknown) => {
      const principal = await authenticate(ctx);
      const where = exchanges.getStore();
      if (where !== undefined) {
        callerOf(where).principal = principal === null || principal === undefined ? null : encode(principal, caseNow(), 'caller');
      }
      return principal;
    };
    wrapped.authenticate = (authenticate as { readsAuthorization?: boolean }).readsAuthorization ? Object.assign(recorded, { readsAuthorization: true }) : recorded;
  }
  const authenticateService = wrapped.authenticateService as ((ctx: unknown) => unknown) | undefined;
  if (typeof authenticateService === 'function') {
    wrapped.authenticateService = async (ctx: unknown) => {
      const where = exchanges.getStore();
      try {
        const service = await authenticateService(ctx);
        if (where !== undefined) {
          callerOf(where).service = service === null || service === undefined ? null : encode(service, caseNow(), 'caller');
        }
        return service;
      } catch (error) {
        if (where !== undefined) {
          callerOf(where).serviceRefused = encode({ status: (error as { status?: unknown }).status, code: (error as { code?: unknown }).code }, caseNow(), 'caller');
        }
        throw error;
      }
    };
  }
  return wrapped as O;
}

function callerOf(where: Active): JSONObject {
  const request = where.step.request as JSONObject;
  request.caller ??= {};
  return request.caller as JSONObject;
}

/** How the corpus describes an app the test serves: an engine's, its MCP endpoint, or a parent app that mounts them. */
export interface AppDescription {
  kind: 'engine' | 'mcp' | 'mount';
  /** The engine it serves, as the test opened it. */
  engine?: Engine;
  /** The options the test built it with. */
  options?: object;
  /** For a parent app: what it mounts, and where. */
  mounts?: Array<{ prefix: string; kind: 'engine' | 'mcp'; options?: object }>;
}

/**
 * recordedApp records every exchange an app serves, through its fetch (and
 * so its request), as a step of the test's case. Build the app over the
 * raw engine with recordedCallers' options (appFor does both), so the
 * app's own calls are not steps. Without ENGINE_RECORD it returns the app.
 */
export function recordedApp<A extends Fetching>(app: A, describe: AppDescription): A {
  if (!recording) {
    return app;
  }
  const record = caseNow();
  record.http = true;
  record.apps += 1;
  const label = `a${record.apps}`;
  const step: JSONObject = { app: label, kind: describe.kind };
  const engineLabel = describe.engine === undefined ? undefined : engineLabels.get(describe.engine);
  if (engineLabel !== undefined) {
    step.engine = engineLabel;
  }
  if (describe.options !== undefined) {
    step.options = appOptions(describe.options, record);
  }
  if (describe.mounts !== undefined) {
    step.mounts = describe.mounts.map((mount) => ({ prefix: mount.prefix, kind: mount.kind, ...(mount.options ? { options: appOptions(mount.options, record) } : {}) }));
  }
  record.steps.push(step);
  const fetch = app.fetch.bind(app);
  const recorded = (request: Request, ...rest: unknown[]) => exchange(label, request, () => fetch(request, ...rest));
  Object.defineProperty(app, 'fetch', { value: recorded, configurable: true, writable: true });
  return app;
}

/**
 * appFor builds an app over the engine a test opened with build (engineApp
 * or engineMcp): over the raw engine, with the callers recorded, so it can
 * be mounted under a recorded parent app.
 */
export function appFor<O, A>(build: (engine: Engine, options: O) => A, engine: Engine, options: NoInfer<O>): A {
  return build(rawEngine(engine), recordedCallers(options as object) as O);
}

/** servedApp is appFor's app, recorded: what a test that serves one app on its own builds. */
export function servedApp<O, A extends Fetching>(
  build: (engine: Engine, options: O) => A,
  engine: Engine,
  options: NoInfer<O>,
  kind: 'engine' | 'mcp' = 'engine'
): A {
  return recordedApp(appFor(build, engine, options), { kind, engine, options: options as object });
}

// The labels of the engines the recorder opened, by proxy.
const engineLabels = new WeakMap<object, string>();

function appOptions(options: object, record: Case): JSONValue {
  const out: JSONObject = {};
  for (const [key, value] of Object.entries(options)) {
    if (value === undefined) {
      continue;
    }
    if (typeof value === 'function') {
      // The host's: authenticate and authenticateService are recorded per
      // exchange as its caller.
      out[key] = key === 'authenticate' || key === 'authenticateService' ? 'host' : 'custom';
      if (key !== 'authenticate' && key !== 'authenticateService') {
        record.js.add(`app.${key}`);
      }
      continue;
    }
    if (key === 'identity') {
      out[key] = 'host';
      record.js.add('app.identity');
      continue;
    }
    out[key] = encode(value, record, 'options');
  }
  return out;
}

async function exchange(app: string, request: Request, serve: () => Response | Promise<Response>): Promise<Response> {
  const record = caseNow();
  const url = new URL(request.url);
  const recordedRequest: JSONObject = { method: request.method, path: url.pathname };
  if (url.search !== '') {
    recordedRequest.query = url.search.slice(1);
  }
  const headers: JSONObject = {};
  for (const name of REQUEST_HEADERS) {
    const value = request.headers.get(name);
    if (value !== null) {
      headers[name] = value;
    }
  }
  if (Object.keys(headers).length > 0) {
    recordedRequest.headers = headers;
  }
  let text = '';
  if (request.body !== null) {
    text = await request.clone().text();
    if (text !== '') {
      recordedRequest.body = bodyOf(text, request.headers.get('content-type'));
    }
  }
  const step: JSONObject = { request: recordedRequest };
  if (record.apps > 1) {
    step.app = app;
  }
  // The step takes its place when the request comes in, so what the
  // request sets off (an event a stream sends) comes after it.
  record.steps.push(step);
  const here: Active = { step, engine: record.engines[record.engines.length - 1], readings: [], ids: [] };
  let response: Response;
  try {
    response = await exchanges.run(here, async () => serve());
  } catch (error) {
    step.error = encodeError(error, record);
    throw error;
  }
  const recordedResponse: JSONObject = { status: response.status };
  const answered: JSONObject = {};
  for (const name of RESPONSE_HEADERS) {
    const value = response.headers.get(name);
    if (value !== null) {
      answered[name] = value;
    }
  }
  if (Object.keys(answered).length > 0) {
    recordedResponse.headers = answered;
  }
  step.response = recordedResponse;
  settle(record, here);
  const type = response.headers.get('content-type') ?? '';
  if (response.body === null) {
    return response;
  }
  if (type.startsWith('text/event-stream')) {
    record.streams += 1;
    const label = `s${record.streams}`;
    step.stream = label;
    return new Response(streamBody(label, response.body, record, request.signal), { status: response.status, statusText: response.statusText, headers: response.headers });
  }
  const body = await response.text();
  if (body !== '') {
    recordedResponse.body = bodyOf(body, type);
  }
  return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers });
}

function bodyOf(text: string, type: string | null): JSONValue {
  if (type !== null && /json/u.test(type)) {
    try {
      return JSON.parse(text) as JSONValue;
    } catch {
      return { text };
    }
  }
  return { text };
}

// streamBody passes an event stream through, recording its frames as the
// client takes them: consecutive frames with no step between them are one
// step, `{"stream", "frames"}`. The server's end of the body, while the
// client is still there, is `{"stream", "end": true}`. A client's
// disconnect is not a step: when the server learns of it races with the
// test's next step, and it changes nothing a step reads.
function streamBody(label: string, body: ReadableStream<Uint8Array>, record: Case, signal: AbortSignal): ReadableStream<Uint8Array> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let cancelled = false;
  const push = (frames: JSONValue[]): void => {
    if (frames.length === 0) {
      return;
    }
    const last = record.steps[record.steps.length - 1];
    if (last !== undefined && last.stream === label && Array.isArray(last.frames) && last.request === undefined) {
      (last.frames as JSONValue[]).push(...frames);
    } else {
      record.steps.push({ stream: label, frames });
    }
  };
  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      const { done, value } = await reader.read();
      if (done) {
        if (!signal.aborted && !cancelled) {
          record.steps.push({ stream: label, end: true });
        }
        controller.close();
        return;
      }
      buffer += decoder.decode(value, { stream: true });
      const frames: JSONValue[] = [];
      let end = buffer.indexOf('\n\n');
      while (end >= 0) {
        const frame = frameOf(buffer.slice(0, end));
        // A heartbeat's comment comes on a timer, not after a step: how many
        // a test sees is its timing, so the corpus leaves them out.
        if (!(isObject(frame) && frame.comment === 'keepalive' && Object.keys(frame).length === 1)) {
          frames.push(frame);
        }
        buffer = buffer.slice(end + 2);
        end = buffer.indexOf('\n\n');
      }
      push(frames);
      controller.enqueue(value);
    },
    async cancel(reason) {
      cancelled = true;
      await reader.cancel(reason);
    },
  });
}

// frameOf reads one server-sent event: its comment, event name, id and
// data, the data parsed when it is JSON.
function frameOf(block: string): JSONValue {
  const frame: JSONObject = {};
  for (const line of block.split('\n')) {
    if (line.startsWith(':')) {
      frame.comment = line.slice(1).trim();
    } else if (line.startsWith('event: ')) {
      frame.event = line.slice(7);
    } else if (line.startsWith('id: ')) {
      frame.id = line.slice(4);
    } else if (line.startsWith('data: ')) {
      const data = line.slice(6);
      try {
        frame.data = JSON.parse(data) as JSONValue;
      } catch {
        frame.text = data;
      }
    }
  }
  return frame;
}

/**
 * endCase ends the test's case: cleanup calls it with the test's context,
 * before it closes what the test opened.
 */
export function endCase(context?: TestContextLike): void {
  if (!recording) {
    return;
  }
  const record = current;
  current = undefined;
  active = undefined;
  depth = 0;
  drawn = 0;
  if (record === undefined || record.steps.length === 0) {
    return;
  }
  const filePath = context?.filePath ?? 'unknown';
  const file = basename(filePath).replace(/\.test\.ts$/u, '').replace(/\.ts$/u, '');
  let entry = files.get(file);
  if (entry === undefined) {
    entry = { source: filePath, cases: [] };
    files.set(file, entry);
  }
  const name = context?.fullName ?? context?.name ?? `case ${entry.cases.length + 1}`;
  holdClocksApart(name, record);
  entry.cases.push({ name, case: record });
}

// holdClocksApart refuses a case in which a fixed clock's span overlaps
// the wall clock's: symbolize would write a fixed clock's time in the wall
// clock's span as a symbol, and a replayer could not set its clock to it.
// A test that opens engines on both gives its fixed clock times far from
// today's (7, 1000), as every test does.
function holdClocksApart(name: string, record: Case): void {
  if (record.wallReadings.length === 0 || record.fixedReadings.length === 0) {
    return;
  }
  const wall = windowOf(record.wallReadings);
  const fixed = windowOf(record.fixedReadings);
  if (fixed.from <= wall.to && wall.from <= fixed.to) {
    throw new Error(
      `the recorder cannot tell the times of "${name}" apart: its fixed clock read from ${Math.min(...record.fixedReadings)} to ${Math.max(...record.fixedReadings)}, ` +
        `within 400 days of what the wall clock read (from ${Math.min(...record.wallReadings)}); give the test's clock times far from today's`
    );
  }
}

// Writing: each case's symbols, then each file.

function writeFiles(): void {
  if (RECORD_DIR === undefined) {
    return;
  }
  for (const [file, entry] of files) {
    const http = entry.cases.some(({ case: record }) => record.http);
    const listKey = http ? 'cases' : 'scenarios';
    const directory = join(RECORD_DIR, http ? 'http' : 'scenarios');
    mkdirSync(directory, { recursive: true });
    const source = relative(join(resolve(new URL('.', import.meta.url).pathname), '..', '..', '..', '..'), entry.source);
    const finished = entry.cases.map(({ name, case: record }) => finish(name, record));
    // A case past MAX_CASE_CHARS (one that creates hundreds of instances
    // to reach a limit, say) is named in `omitted` rather than written.
    const cases = finished.filter((scenario) => JSON.stringify(scenario).length <= MAX_CASE_CHARS);
    const omitted = finished.filter((scenario) => JSON.stringify(scenario).length > MAX_CASE_CHARS).map((scenario) => scenario.name);
    const { shared, cases: sharing } = share(cases);
    const document: Record<string, unknown> = { name: file, description: describeFile(entry.source), source };
    if (omitted.length > 0) {
      document.omitted = omitted;
    }
    if (Object.keys(shared).length > 0) {
      document.shared = shared;
    }
    document[listKey] = sharing;
    writeFileSync(join(directory, `${file}.json`), render(document, listKey));
  }
}

/** The most JSON text, in characters, of a case the corpus writes (before its shared values are taken out). */
const MAX_CASE_CHARS = 100_000;

// The members of a step whose own members, not they, are shared.
const OPENED = new Set(['args', 'request', 'response']);

/** The least JSON text, in characters, of a value the corpus writes once in a file's shared table. */
const SHARED_MIN_CHARS = 400;

// share writes a value that appears more than once in a file's cases, and
// whose JSON is at least SHARED_MIN_CHARS long, once, in the file's
// `shared` table, and each appearance as {"$shared": "s<N>"}, numbered by
// first appearance: a describe document a test reads again, a schema
// document each test defines. A value inside a shared one stays in it.
function share(cases: JSONObject[]): { shared: JSONObject; cases: JSONObject[] } {
  const counts = new Map<string, number>();
  const count = (value: JSONValue): void => {
    if (value === null || typeof value === 'number' || typeof value === 'boolean') {
      return;
    }
    const text = JSON.stringify(value);
    if (text.length >= SHARED_MIN_CHARS) {
      counts.set(text, (counts.get(text) ?? 0) + 1);
    }
    if (Array.isArray(value)) {
      value.forEach(count);
    } else if (typeof value === 'object') {
      Object.values(value).forEach(count);
    }
  };
  // Values inside steps, not whole steps, so each step reads as a step; and
  // a request, a response and the arguments are opened a level further, so
  // what a step sends reads in it.
  const members = (step: JSONValue): JSONValue[] =>
    isObject(step) ? Object.entries(step as JSONObject).flatMap(([key, value]) => (OPENED.has(key) && value !== null && typeof value === 'object' ? Object.values(value) : [value])) : [];
  for (const scenario of cases) {
    ((scenario.steps as JSONValue[]) ?? []).forEach((step) => members(step).forEach(count));
  }
  const names = new Map<string, string>();
  const table = new Map<string, JSONValue>();
  const uses = new Map<string, number>();
  const replace = (value: JSONValue): JSONValue => {
    if (value === null || typeof value === 'number' || typeof value === 'boolean') {
      return value;
    }
    const text = JSON.stringify(value);
    if (text.length >= SHARED_MIN_CHARS && (counts.get(text) ?? 0) > 1) {
      let name = names.get(text);
      if (name === undefined) {
        name = `s${names.size + 1}`;
        names.set(text, name);
        table.set(name, value);
      }
      uses.set(name, (uses.get(name) ?? 0) + 1);
      return { $shared: name };
    }
    if (Array.isArray(value)) {
      return value.map(replace);
    }
    if (typeof value === 'object') {
      const out: JSONObject = {};
      for (const [key, member] of Object.entries(value)) {
        out[key] = replace(member);
      }
      return out;
    }
    return value;
  };
  const replaceStep = (step: JSONValue): JSONValue => {
    const out: JSONObject = {};
    for (const [key, member] of Object.entries(step as JSONObject)) {
      if (OPENED.has(key) && Array.isArray(member)) {
        out[key] = member.map(replace);
      } else if (OPENED.has(key) && isObject(member)) {
        out[key] = Object.fromEntries(Object.entries(member as JSONObject).map(([name, value]) => [name, replace(value)]));
      } else {
        out[key] = replace(member);
      }
    }
    return out;
  };
  const replaced = cases.map((scenario) => ({ ...scenario, steps: ((scenario.steps as JSONValue[]) ?? []).map(replaceStep) }));
  // A value counted again only inside copies of a larger shared one is
  // used once outside it: write it in place, and number the rest again.
  const renamed = new Map<string, string>();
  const shared: JSONObject = {};
  const settle = (value: JSONValue): JSONValue => {
    if (Array.isArray(value)) {
      return value.map(settle);
    }
    if (value !== null && typeof value === 'object') {
      const name = typeof value.$shared === 'string' && Object.keys(value).length === 1 ? value.$shared : undefined;
      if (name !== undefined) {
        if ((uses.get(name) ?? 0) < 2) {
          return table.get(name) as JSONValue;
        }
        let final = renamed.get(name);
        if (final === undefined) {
          final = `s${renamed.size + 1}`;
          renamed.set(name, final);
          shared[final] = table.get(name) as JSONValue;
        }
        return { $shared: final };
      }
      const out: JSONObject = {};
      for (const [key, member] of Object.entries(value)) {
        out[key] = settle(member);
      }
      return out;
    }
    return value;
  };
  return { shared, cases: replaced.map((scenario) => ({ ...scenario, steps: (scenario.steps as JSONValue[]).map(settle) })) };
}

// describeFile is the test file's leading comment, which says what its
// tests cover.
function describeFile(path: string): string {
  try {
    const lines: string[] = [];
    for (const line of readFileSync(path, 'utf8').split('\n')) {
      if (!line.startsWith('//')) {
        break;
      }
      lines.push(line.replace(/^\/\/ ?/u, ''));
    }
    return lines.join(' ').replace(/\s+/gu, ' ').trim();
  } catch {
    return '';
  }
}

function finish(name: string, record: Case): JSONObject {
  for (const engine of record.engines) {
    const options = (engine.open.args as JSONObject[])[0];
    if (engine.answers !== undefined && isObject(options.policy)) {
      options.policy = { answers: [...engine.answers.values()].map(({ request, answer }) => [request, answer]) };
    }
  }
  const single = record.engines.length <= 1;
  const steps = record.steps.map((step) => {
    if (single && step.engine !== undefined && step.app === undefined) {
      const { engine: _engine, ...rest } = step;
      return rest;
    }
    if (single && step.engine !== undefined) {
      const { engine: _engine, ...rest } = step;
      return rest;
    }
    return step;
  });
  for (const step of steps) {
    if (step.call !== undefined && isSearchCall(step, record)) {
      step.dialect = 'sqlite';
    }
  }
  const core = new Set(record.engines.flatMap((engine) => [...engine.core]));
  const scenario: JSONObject = { name };
  const behaviors = [...record.composed].sort();
  if (behaviors.length > 0) {
    scenario.behaviors = behaviors;
  }
  // The behaviors the test defined in TypeScript that its results depend
  // on: one a schema composes or a step registers, or every one when a step
  // reads the registered behaviors.
  const readsCatalog = steps.some((step) => typeof step.call === 'string' && CATALOG_CALLS.has(step.call));
  const testBehaviors = [...record.testBehaviors]
    .filter((behavior) => !core.has(behavior) && (readsCatalog || record.composed.has(behavior) || record.registered.has(behavior)))
    .sort();
  if (testBehaviors.length > 0) {
    scenario.testBehaviors = testBehaviors;
  }
  const js = [...record.js].sort();
  if (js.length > 0) {
    scenario.js = js;
  }
  // A case that writes the file through SQL of its own runs on SQLite
  // alone; elsewhere a step marked sqlite is one whose answer is SQLite's.
  if (steps.some((step) => step.call === 'storage.run' || step.call === 'storage.exec' || step.call === 'storage.transaction')) {
    scenario.dialect = 'sqlite';
  }
  const principals: JSONObject = {};
  for (const { name: principal, value } of record.principals.values()) {
    principals[principal] = value;
  }
  if (Object.keys(principals).length > 0) {
    scenario.principals = principals;
  }
  scenario.steps = steps;
  return symbolize(scenario, record) as JSONObject;
}

// The calls that read which behaviors the engine has registered.
const CATALOG_CALLS = new Set(['tools.listBehaviors', 'tools.describeBehavior', 'behaviors.names', 'behaviors.has', 'behaviors.declaration']);

// Search's operations rank by SQLite's FTS5 bm25: a step that calls one,
// or the search across schemas, depends on SQLite.
const SEARCH_OPERATIONS = new Set(['search', 'similar']);

function isSearchCall(step: JSONObject, record: Case): boolean {
  if (!record.composed.has('Search')) {
    return false;
  }
  if (step.call === 'search') {
    return true;
  }
  const args = (step.args as JSONValue[] | undefined) ?? [];
  if (step.call === 'instances.invoke' || step.call === 'instances.operate') {
    return SEARCH_OPERATIONS.has(String(args[2]));
  }
  if (step.call === 'instances.invokeSchema') {
    return SEARCH_OPERATIONS.has(String(args[1]));
  }
  if (step.call === 'tools.call') {
    return /(^|[_-])(search|similar)([_-]|$)/u.test(String(args[0]));
  }
  return false;
}

// symbolize writes what differs between runs as symbols: each id the
// generator drew as {{id:N}} (its base62 form as {{id:N:62}}), numbered by
// first appearance; each temporary directory as {{dir:N}}; an opaque
// cursor that encodes either as {{opaque:N}}; and, in a case that read the
// wall clock, each instant that clock gave, or a time computed from one
// (an integer of epoch milliseconds, an RFC 3339 date-time, or, as
// {{time:N:us}}, an integer of epoch microseconds) as {{time:N}}, numbered
// by instant in order of first appearance. A time is a value in the wall
// clock's span, which its readings alone make: a fixed clock's never widen
// it, so it starts a day before today and holds no small integer (a seq, a
// count, a fixed clock's 7), and endCase refuses a case whose fixed clock
// reads near it.
function symbolize(scenario: JSONValue, record: Case): JSONValue {
  const window = record.wallReadings.length > 0 ? windowOf(record.wallReadings) : undefined;
  const given = givenValues(scenario);
  const numbering = (): ((key: string | number) => number) => {
    const seen = new Map<string | number, number>();
    return (key) => {
      let n = seen.get(key);
      if (n === undefined) {
        n = seen.size + 1;
        seen.set(key, n);
      }
      return n;
    };
  };
  const idOf = numbering();
  const dirOf = numbering();
  const timeOf = numbering();
  const opaqueOf = numbering();
  const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gu;
  const BASE62 = /(?<![0-9A-Za-z])[0-9A-Za-z]{18,22}(?![0-9A-Za-z])/gu;
  const DATE_TIME = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})/gu;
  const EPOCH = /(?<!\d)\d{13}(?:\d{3})?(?!\d)/gu;
  // timeUnit says whether a value is an instant of the wall clock's span
  // the test did not give, and as which unit.
  const timeUnit = (value: number): 'ms' | 'us' | undefined => {
    if (window === undefined || !Number.isInteger(value) || given.numbers.has(value)) {
      return undefined;
    }
    return inWindow(value, window) ? 'ms' : inWindow(value / 1000, window) ? 'us' : undefined;
  };
  const isDateTime = (text: string): boolean => window !== undefined && !given.strings.has(text) && inWindow(Date.parse(text), window);
  const hasSymbols = (text: string): boolean =>
    directories.some((directory) => text.includes(directory)) ||
    [...text.matchAll(UUID)].some(([match]) => generated.has(match)) ||
    [...text.matchAll(BASE62)].some(([match]) => generated62.has(match)) ||
    [...text.matchAll(DATE_TIME)].some(([match]) => isDateTime(match)) ||
    [...text.matchAll(EPOCH)].some(([match]) => timeUnit(Number(match)) !== undefined);
  const timeToken = (value: number, unit: 'ms' | 'us'): string => (unit === 'ms' ? `{{time:${timeOf(value)}}}` : `{{time:${timeOf(value / 1000)}:us}}`);
  const replace = (text: string): string => {
    let out = text;
    for (const directory of directories) {
      if (out.includes(directory)) {
        out = out.split(directory).join(`{{dir:${dirOf(directory)}}}`);
      }
    }
    out = out.replace(UUID, (match) => (generated.has(match) ? `{{id:${idOf(match)}}}` : match));
    out = out.replace(BASE62, (match) => {
      const id = generated62.get(match);
      return id === undefined ? match : `{{id:${idOf(id)}:62}}`;
    });
    if (window !== undefined) {
      out = out.replace(DATE_TIME, (match) => (isDateTime(match) ? `{{time:${timeOf(Date.parse(match))}}}` : match));
      out = out.replace(EPOCH, (match) => {
        const unit = timeUnit(Number(match));
        return unit === undefined ? match : timeToken(Number(match), unit);
      });
    }
    return out;
  };
  const visit = (value: JSONValue): JSONValue => {
    if (typeof value === 'string') {
      if (/^[A-Za-z0-9_-]{16,}$/u.test(value) && !generated62.has(value)) {
        const decoded = Buffer.from(value, 'base64url').toString('utf8');
        if (/^[[{]|^after:/u.test(decoded) && hasSymbols(decoded)) {
          return `{{opaque:${opaqueOf(value)}}}`;
        }
      }
      return replace(value);
    }
    if (typeof value === 'number') {
      const unit = timeUnit(value);
      return unit === undefined ? value : timeToken(value, unit);
    }
    if (Array.isArray(value)) {
      return value.map(visit);
    }
    if (value !== null && typeof value === 'object') {
      const out: JSONObject = {};
      for (const [key, member] of Object.entries(value)) {
        out[key] = visit(member);
      }
      return out;
    }
    return value;
  };
  return visit(scenario);
}

interface Window {
  readonly from: number;
  readonly to: number;
}

const DAY_MS = 86_400_000;

// windowOf is the span a clock's times fall in: from a day before its
// first reading to 400 days after its last, which a deadline, a retry or
// a schedule the engine computes from the clock stays inside.
function windowOf(readings: readonly number[]): Window {
  return { from: Math.min(...readings) - DAY_MS, to: Math.max(...readings) + 400 * DAY_MS };
}

function inWindow(value: number, window: Window): boolean {
  return Number.isFinite(value) && value >= window.from && value <= window.to;
}

// givenValues collects the numbers and date-times the test itself passed,
// in arguments and requests, which are kept as written.
function givenValues(scenario: JSONValue): { numbers: Set<number>; strings: Set<string> } {
  const numbers = new Set<number>();
  const strings = new Set<string>();
  const collect = (value: JSONValue): void => {
    if (typeof value === 'number') {
      numbers.add(value);
    } else if (typeof value === 'string') {
      strings.add(value);
      for (const match of value.matchAll(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})/gu)) {
        strings.add(match[0]);
      }
    } else if (Array.isArray(value)) {
      value.forEach(collect);
    } else if (value !== null && typeof value === 'object') {
      Object.values(value).forEach(collect);
    }
  };
  const steps = (scenario as JSONObject).steps as JSONObject[];
  for (const step of steps) {
    if (step.call === 'open') {
      continue;
    }
    if (step.args !== undefined) {
      collect(step.args);
    }
    if (step.request !== undefined) {
      collect(step.request);
    }
  }
  return { numbers, strings };
}

// render writes a corpus file: two-space indentation down to each case,
// and each step on a line of its own, so a diff shows the steps that
// changed.
function render(document: Record<string, unknown>, listKey: string): string {
  const lines: string[] = ['{'];
  const entries = Object.entries(document);
  entries.forEach(([key, value], index) => {
    const comma = index < entries.length - 1 ? ',' : '';
    if (key === 'shared') {
      const shared = Object.entries(value as JSONObject);
      lines.push('  "shared": {');
      shared.forEach(([name, entry], entryIndex) => lines.push(`    ${JSON.stringify(name)}: ${JSON.stringify(entry)}${entryIndex < shared.length - 1 ? ',' : ''}`));
      lines.push(`  }${comma}`);
      return;
    }
    if (key !== listKey) {
      lines.push(`  ${JSON.stringify(key)}: ${JSON.stringify(value)}${comma}`);
      return;
    }
    const cases = value as JSONObject[];
    lines.push(`  ${JSON.stringify(key)}: [`);
    cases.forEach((scenario, caseIndex) => {
      lines.push('    {');
      const members = Object.entries(scenario);
      members.forEach(([member, memberValue], memberIndex) => {
        const memberComma = memberIndex < members.length - 1 ? ',' : '';
        if (member === 'steps') {
          const steps = memberValue as JSONValue[];
          lines.push('      "steps": [');
          steps.forEach((step, stepIndex) => lines.push(`        ${JSON.stringify(step)}${stepIndex < steps.length - 1 ? ',' : ''}`));
          lines.push(`      ]${memberComma}`);
        } else {
          lines.push(`      ${JSON.stringify(member)}: ${JSON.stringify(memberValue)}${memberComma}`);
        }
      });
      lines.push(`    }${caseIndex < cases.length - 1 ? ',' : ''}`);
    });
    lines.push(`  ]${comma}`);
  });
  lines.push('}');
  return `${lines.join('\n')}\n`;
}
