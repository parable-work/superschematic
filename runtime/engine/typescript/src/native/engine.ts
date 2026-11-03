/*
The engine (openEngine, D58): its synchronous, principal-first API over
the Rust engine's Node-API addon. Each method writes its call by name
with JSON arguments to the addon's callSync, which makes it on this
thread, and reads its result back, or rethrows its error as the
package's class (src/errors.ts). It keeps the API the TypeScript engine
had (D16), which the package's suite holds it to (test/); what the Rust
engine does not do throws UnsupportedError, naming the gap.

On Postgres a synchronous call would hold this thread while it waits on
the database, so the addon refuses it (sync_on_async_storage): an engine
on Postgres is openEngineAsync's (src/native/async.ts), this API with
every call a promise.
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';

import type { AccessRequest, Principal } from '../access.js';
import { EngineError, type ValidationIssue } from '../errors.js';
import type { AnyBehaviorImplementation } from '../testing/behavior.js';
import type { SchemaSearchHit } from '../types/core.js';
import type { BehaviorDeclaration } from '../types/declarations.js';
import type { EventPage, EventWatcher, ReadEventsOptions } from '../types/events.js';
import type {
  CreateOptions,
  DeleteOptions,
  GetOptions,
  InstancePage,
  InstanceRecord,
  InvokeOptions,
  InvokeSchemaOptions,
  ListOptions,
  OperationOutcome,
  UpdateOptions,
} from '../types/instances.js';
import { DEFAULT_NAMESPACE, type NamespaceRecord } from '../types/namespaces.js';
import type { Page } from '../types/paging.js';
import type { PruneResult, RunnerPass, RunnerStatus, SubscriptionKey, SubscriptionStatus } from '../types/runner.js';
import type { ComposedBehavior, DefineOptions, PublishResult, SchemaCapabilities, SchemaRecord, SchemaSummary, SchemaTarget, ValidateOptions } from '../types/schemas.js';
import type { NamespaceOperationInput, SendSignalInput } from '../types/signals.js';
import type { Row, RunResult, SqlValue } from '../types/storage.js';
import type { BehaviorDocument, BehaviorSummary, DescribeDocument, ToolFilter, ToolManifest, ToolSummary, ToolTarget } from '../types/tools.js';
import type { StoredValue, ValueTarget } from '../types/values.js';
import { loadAddon, type Addon, type AddonEngine, type HttpAnswer, type StreamPull } from './addon.js';
import { Bridge, rowOf, sqlParams } from './bridge.js';
import { carry } from './carry.js';
import { UnsupportedError, restoreError } from './errors.js';
import { deepFreeze } from './json.js';
import { nativeOptions, type EngineOptions, type HostedOptions } from './options.js';
import { openConnection, type SqliteConnection } from './sqlite.js';
import { resolveToolOptions, type ResolvedToolOptions } from './tool-options.js';

// Hosts answers what the options leave to TypeScript (a policy, a
// permission matcher, an id source and, with it, a behavior's own UUIDs
// from crypto.randomUUID), which the addon asks on this thread during a
// synchronous call: host(kind, json) -> json. It never throws into the
// addon: it keeps the first error the TypeScript code threw, and the call
// rethrows it once the addon returns, as the TypeScript engine would have
// thrown it from inside the call. A policy's answer is checked as the
// TypeScript engine's Access checks it.
class Hosts {
  private parked: { error: unknown } | undefined;
  /** The tool filter of the call this thread is making, if it has one. */
  filter: ToolFilter | undefined;

  constructor(
    private readonly hosted: HostedOptions,
    /** The bridge to behaviors written in TypeScript, on the test addon. */
    readonly bridge: Bridge
  ) {}

  readonly answer = (kind: string, payload: string): string => {
    if (kind === 'behavior') {
      // The bridge writes what a hook threw into its answer.
      return this.bridge.answer(payload);
    }
    try {
      switch (kind) {
        case 'policy': {
          const answer: unknown = this.hosted.policy?.(JSON.parse(payload) as AccessRequest);
          if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
            throw new TypeError('an access policy is synchronous: it returned a promise');
          }
          return answer === true ? 'true' : 'false';
        }
        case 'toolFilter': {
          const [principal, tool, namespace] = JSON.parse(payload) as [Principal, ToolSummary, string];
          const answer: unknown = this.filter?.(principal, tool, namespace);
          if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
            throw new TypeError('a tool filter is synchronous: it returned a promise');
          }
          return answer === true ? 'true' : 'false';
        }
        case 'ids': {
          const id: unknown = this.hosted.ids?.();
          return JSON.stringify({ id: typeof id === 'string' ? id : String(id) });
        }
        case 'uuid':
          // A behavior's own UUIDs on an engine whose ids this source makes:
          // crypto.randomUUID, where the TypeScript engine's version graph
          // draws them.
          return globalThis.crypto.randomUUID();
        case 'permissionMatcher': {
          const [held, required] = JSON.parse(payload) as [string[], string[]];
          const answer: unknown = this.hosted.permissionMatcher?.(held, required);
          if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
            throw new TypeError('a permission matcher is synchronous: it returned a promise');
          }
          return answer === true ? 'true' : 'false';
        }
        default:
          throw new Error(`the Rust engine asked the facade an unknown question: ${kind}`);
      }
    } catch (error) {
      this.parked ??= { error };
      return kind === 'ids' ? '{}' : kind === 'uuid' ? '' : 'false';
    }
  };

  /** rethrow throws what the TypeScript code threw during the call, if it threw. */
  rethrow(): void {
    const parked = this.parked;
    this.parked = undefined;
    if (parked !== undefined) {
      throw parked.error;
    }
  }
}

// The calls that never read the engine's clock on the TypeScript engine.
const TIMELESS: ReadonlySet<string> = new Set([
  'behaviors.has',
  'behaviors.names',
  'behaviors.declaration',
  'namespaces.names',
  'events.head',
  'events.floor',
  'events.read',
  'schemas.live',
  'schemas.draft',
  'schemas.version',
  'schemas.list',
  'namespaces.list',
  'namespaces.get',
  'tools.listBehaviors',
  'tools.describeBehavior',
  'values.thresholdBytes',
  'values.maxBytes',
  'runner.running',
  'runner.nextWake',
  'runner.start',
  'runner.stop',
  'api.routes',
]);

// Calls makes the engine's calls by name: { op, principal, args, now }.
class Calls {
  constructor(
    private readonly addon: AddonEngine,
    private readonly clock: (() => number) | undefined,
    private readonly hosts: Hosts,
    private readonly watchers: Watchers
  ) {}

  sync(op: string, principal: Principal | undefined, args: readonly unknown[] = [], filter?: ToolFilter): unknown {
    let text: string;
    const bridge = this.hosts.bridge;
    const outer = this.hosts.filter;
    this.hosts.filter = filter;
    try {
      text = this.addon.callSync(this.request(op, principal, args, filter !== undefined));
    } catch (error) {
      const restored = restoreError(error, (token) => bridge.parkedValue(token));
      this.hosts.filter = outer;
      bridge.settle();
      this.watchers.poll();
      this.hosts.rethrow();
      throw restored;
    }
    this.hosts.filter = outer;
    bridge.settle();
    this.watchers.poll();
    this.hosts.rethrow();
    return text === '' ? undefined : JSON.parse(text);
  }

  /**
   * http serves one HTTP request on this thread: its head (as the addon's
   * handleHttp takes it) and body, through the mount's tool filter. What a
   * behavior written in TypeScript threw while it ran comes back beside the
   * answer, for the mount's onError.
   */
  http(head: Record<string, unknown>, body: Buffer | undefined, filter: ToolFilter | undefined): { answer: HttpAnswer; thrown?: { value: unknown } } {
    const bridge = this.hosts.bridge;
    const outer = this.hosts.filter;
    this.hosts.filter = filter;
    let answer: HttpAnswer;
    let thrown: { value: unknown } | undefined;
    try {
      // A clock function is read once per request, as for a call.
      answer = this.addon.handleHttp(JSON.stringify({ ...head, toolFilter: filter !== undefined, now: this.clock?.() }), body);
      thrown = answer.status === 500 ? bridge.firstParked() : undefined;
    } catch (error) {
      const restored = restoreError(error, (token) => bridge.parkedValue(token));
      this.hosts.filter = outer;
      bridge.settle();
      this.watchers.poll();
      this.hosts.rethrow();
      throw restored;
    }
    this.hosts.filter = outer;
    bridge.settle();
    this.watchers.poll();
    this.hosts.rethrow();
    return thrown === undefined ? { answer } : { answer, thrown };
  }

  async async(op: string, principal: Principal | undefined, args: readonly unknown[] = []): Promise<unknown> {
    let text: string;
    try {
      text = await this.addon.call(this.request(op, principal, args));
    } catch (error) {
      throw restoreError(error);
    }
    return text === '' ? undefined : JSON.parse(text);
  }

  private request(op: string, principal: Principal | undefined, args: readonly unknown[], toolFilter = false): string {
    // A trailing argument left out is one the call defaults.
    let end = args.length;
    while (end > 0 && args[end - 1] === undefined) {
      end -= 1;
    }
    // A clock function is read once for a call that may use the time, as
    // the TypeScript engine reads it, and not for one that only reads what
    // the engine holds, so a clock that counts its readings counts alike.
    const now = TIMELESS.has(op) ? undefined : this.clock?.();
    return JSON.stringify({ op, principal, args: carry(args.slice(0, end)), now, ...(toolFilter ? { toolFilter: true } : {}) });
  }
}

// targetOf is a tool target as JSON carries it: its filter, a function,
// goes beside it, which the addon asks on this thread.
function targetOf(target: ToolTarget | undefined): { target: Omit<ToolTarget, 'filter'> | undefined; filter: ToolFilter | undefined } {
  if (target === undefined) {
    return { target: undefined, filter: undefined };
  }
  const { filter, ...rest } = target;
  if (filter !== undefined && typeof filter !== 'function') {
    throw new TypeError('a tool filter is a function (principal, tool, namespace) => boolean');
  }
  return { target: rest, filter };
}

class EngineNamespaces {
  constructor(private readonly calls: Calls) {}

  get names(): readonly string[] {
    return this.calls.sync('namespaces.names', undefined) as string[];
  }

  /** resolve returns the namespace a call names, `default` when it names none. */
  resolve(namespace: string | undefined): string {
    if (namespace === undefined) {
      return DEFAULT_NAMESPACE;
    }
    if (!this.names.includes(namespace)) {
      throw new EngineError('unknown_namespace', `unknown namespace "${String(namespace)}"`);
    }
    return namespace;
  }

  list(principal: Principal): NamespaceRecord[] {
    return this.calls.sync('namespaces.list', principal) as NamespaceRecord[];
  }

  get(principal: Principal, name: string): NamespaceRecord {
    return this.calls.sync('namespaces.get', principal, [name]) as NamespaceRecord;
  }

  create(principal: Principal, name: string): NamespaceRecord {
    return this.calls.sync('namespaces.create', principal, [name]) as NamespaceRecord;
  }

  archive(principal: Principal, name: string): NamespaceRecord {
    return this.calls.sync('namespaces.archive', principal, [name]) as NamespaceRecord;
  }

  unarchive(principal: Principal, name: string): NamespaceRecord {
    return this.calls.sync('namespaces.unarchive', principal, [name]) as NamespaceRecord;
  }
}

/**
 * A version's validator on the Rust engine: the TypeScript
 * SchemaValidator's model and validate, built once per version, under the
 * TypeScript class's name, as a recording names a result's class.
 */
class SchemaValidator {
  readonly model: { name: string; instanceType: string; document: Record<string, unknown>; canonical: string };

  constructor(
    record: SchemaRecord,
    private readonly check: (value: unknown) => ValidationIssue[]
  ) {
    this.model = Object.freeze({
      name: record.name,
      instanceType: record.instanceType,
      document: record.document as Record<string, unknown>,
      canonical: record.canonical,
    });
  }

  /** validate returns every issue with value as an instance of the version; empty means valid. */
  validate(value: unknown): ValidationIssue[] {
    return this.check(value);
  }

  normalize(): never {
    throw new UnsupportedError("SchemaValidator.normalize, the TypeScript validator's own", 'validator');
  }

  validateType(): never {
    throw new UnsupportedError("SchemaValidator.validateType, the TypeScript validator's own", 'validator');
  }
}

export { SchemaValidator };

class EngineSchemas {
  constructor(private readonly calls: Calls) {}

  define(principal: Principal, input: string | Record<string, unknown>, options?: DefineOptions): SchemaRecord {
    return this.calls.sync('schemas.define', principal, [input, options]) as SchemaRecord;
  }

  publish(principal: Principal, name: string, options?: SchemaTarget): PublishResult {
    return this.calls.sync('schemas.publish', principal, [name, options]) as PublishResult;
  }

  live(principal: Principal, name: string, options?: SchemaTarget): SchemaRecord | undefined {
    return this.calls.sync('schemas.live', principal, [name, options]) as SchemaRecord | undefined;
  }

  draft(principal: Principal, name: string, options?: SchemaTarget): SchemaRecord | undefined {
    return this.calls.sync('schemas.draft', principal, [name, options]) as SchemaRecord | undefined;
  }

  version(principal: Principal, name: string, version: number, options?: SchemaTarget): SchemaRecord | undefined {
    return this.calls.sync('schemas.version', principal, [name, version, options]) as SchemaRecord | undefined;
  }

  list(principal: Principal, options?: SchemaTarget): SchemaSummary[] {
    return this.calls.sync('schemas.list', principal, [options]) as SchemaSummary[];
  }

  capabilities(principal: Principal, options?: SchemaTarget): SchemaCapabilities[] {
    return this.calls.sync('schemas.capabilities', principal, [options]) as SchemaCapabilities[];
  }

  validate(principal: Principal, name: string, value: unknown, options?: ValidateOptions): ValidationIssue[] {
    return this.calls.sync('schemas.validate', principal, [name, value, options]) as ValidationIssue[];
  }

  /**
   * validator is the validator of a name's live version, or of the given
   * version, as the TypeScript engine's is built once per version: its
   * model, and validate, which holds a value to that version on the Rust
   * engine. It asks read on the schema, as schemas.live and
   * schemas.version do.
   */
  validator(principal: Principal, name: string, options: ValidateOptions = {}): SchemaValidator {
    const target = options.namespace === undefined ? undefined : { namespace: options.namespace };
    const record =
      options.version === undefined ? this.live(principal, name, target) : this.version(principal, name, options.version, target);
    if (record === undefined) {
      const which = options.version === undefined ? 'no live version' : `no version ${options.version}`;
      throw new EngineError('not_found', `schema ${name} has ${which} in namespace ${options.namespace ?? DEFAULT_NAMESPACE}`);
    }
    const key = `${record.namespace}\u0000${record.name}\u0000${String(record.version)}`;
    let validator = this.validators.get(key);
    if (validator === undefined) {
      validator = new SchemaValidator(record, (value) =>
        this.calls.sync('schemas.validate', principal, [record.name, value, { namespace: record.namespace, version: record.version }]) as ValidationIssue[]
      );
      this.validators.set(key, validator);
    }
    return validator;
  }

  private readonly validators = new Map<string, SchemaValidator>();

  behaviors(principal: Principal, name: string, options?: ValidateOptions): ComposedBehavior[] {
    return this.calls.sync('schemas.behaviors', principal, [name, options]) as ComposedBehavior[];
  }
}

class EngineInstances {
  constructor(private readonly calls: Calls) {}

  create(principal: Principal, schema: string, data: unknown, options?: CreateOptions): InstanceRecord {
    return this.calls.sync('instances.create', principal, [schema, data, options]) as InstanceRecord;
  }

  get(principal: Principal, schema: string, id: string, options?: GetOptions): InstanceRecord | undefined {
    return this.calls.sync('instances.get', principal, [schema, id, options]) as InstanceRecord | undefined;
  }

  list(principal: Principal, schema: string, options?: ListOptions): InstancePage {
    return this.calls.sync('instances.list', principal, [schema, options]) as InstancePage;
  }

  lookup(principal: Principal, schema: string, key: unknown, options?: GetOptions): InstanceRecord | undefined {
    return this.calls.sync('instances.lookup', principal, [schema, key, options]) as InstanceRecord | undefined;
  }

  update(principal: Principal, schema: string, id: string, patch: unknown, options?: UpdateOptions): InstanceRecord {
    return this.calls.sync('instances.update', principal, [schema, id, patch, options]) as InstanceRecord;
  }

  delete(principal: Principal, schema: string, id: string, options?: DeleteOptions): boolean {
    return this.calls.sync('instances.delete', principal, [schema, id, options]) as boolean;
  }

  invoke(principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options?: InvokeOptions): unknown {
    return this.calls.sync('instances.invoke', principal, [schema, id, operation, params, options]);
  }

  operate(principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options?: InvokeOptions): OperationOutcome {
    return this.calls.sync('instances.operate', principal, [schema, id, operation, params, options]) as OperationOutcome;
  }

  invokeSchema(principal: Principal, schema: string, operation: string, params: unknown = {}, options?: InvokeSchemaOptions): unknown {
    return this.calls.sync('instances.invokeSchema', principal, [schema, operation, params, options]);
  }
}

// Watchers hears of the commits the engine makes, as the TypeScript
// engine's notifier told them: after each commit that appended events,
// once per event, with its cursor. A call this
// facade makes is followed by a look at the notifier's epoch, so a
// watcher hears of its commits before the call returns, as it would on
// the TypeScript engine; a commit made elsewhere (the runner's thread, an
// asynchronous call, another process on Postgres) reaches it through the
// addon's watch, a weak threadsafe function, which never holds the
// process open.
class Watchers {
  private readonly watchers = new Set<EventWatcher>();
  private epoch = 0;
  private head = 0;
  private polling = false;
  private watching = false;
  closed = false;

  constructor(private readonly addon: AddonEngine) {}

  attach(calls: Calls): void {
    this.calls = calls;
  }

  private calls: Calls | undefined;

  get size(): number {
    return this.watchers.size;
  }

  watch(watcher: EventWatcher): () => void {
    if (this.closed) {
      throw new Error('the engine is closed');
    }
    if (this.watchers.size === 0) {
      this.epoch = this.addon.epoch;
      this.head = this.calls?.sync('events.head', undefined) as number;
    }
    this.watchers.add(watcher);
    this.start();
    return () => {
      this.watchers.delete(watcher);
    };
  }

  /**
   * onWake calls wake each time the addon's watch hears the notifier: a
   * commit, or a signal one of the engine's event streams kept (D77),
   * which tells no watcher of a commit but wakes a stream's body.
   */
  onWake(wake: () => void): () => void {
    if (this.closed) {
      throw new Error('the engine is closed');
    }
    this.wakes.add(wake);
    this.start();
    return () => {
      this.wakes.delete(wake);
    };
  }

  private readonly wakes = new Set<() => void>();

  private start(): void {
    if (!this.watching) {
      this.watching = true;
      this.addon.watch(() => this.heard());
    }
  }

  // heard tells the watchers of the commits, then wakes every stream body.
  private heard(): void {
    this.poll();
    for (const wake of [...this.wakes]) {
      try {
        wake();
      } catch {
        // One stream's failure is not another's.
      }
    }
  }

  /** poll tells the watchers of the commits since it last looked. */
  poll(): void {
    if (this.polling || this.watchers.size === 0 || this.calls === undefined) {
      return;
    }
    let epoch: number;
    try {
      epoch = this.addon.epoch;
    } catch {
      return;
    }
    if (epoch === this.epoch) {
      return;
    }
    this.polling = true;
    try {
      this.epoch = epoch;
      let head: number;
      try {
        head = this.calls.sync('events.head', undefined) as number;
      } catch {
        return;
      }
      for (let cursor = this.head + 1; cursor <= head; cursor += 1) {
        for (const watcher of [...this.watchers]) {
          try {
            watcher.committed(cursor);
          } catch {
            // One watcher's failure is not the writer's, nor another watcher's.
          }
        }
      }
      this.head = Math.max(this.head, head);
    } finally {
      this.polling = false;
    }
  }

  /** close tells every watcher the engine closed, then forgets them. */
  close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    if (this.watching) {
      this.addon.unwatch();
    }
    const watchers = [...this.watchers];
    this.watchers.clear();
    this.wakes.clear();
    for (const watcher of watchers) {
      try {
        watcher.closed?.();
      } catch {
        // As in poll.
      }
    }
  }
}

class EngineEvents {
  constructor(
    private readonly calls: Calls,
    private readonly watchers: Watchers
  ) {}

  read(principal: Principal, options?: ReadEventsOptions): EventPage {
    return this.calls.sync('events.read', principal, [options]) as EventPage;
  }

  head(): number {
    return this.calls.sync('events.head', undefined) as number;
  }

  floor(namespace?: string): number {
    return this.calls.sync('events.floor', undefined, [namespace]) as number;
  }

  /** watch registers a watcher the engine calls after each commit that appended events, and once when it closes. */
  watch(watcher: EventWatcher): () => void {
    return this.watchers.watch(watcher);
  }

  get watching(): number {
    return this.watchers.size;
  }

  close(): void {
    this.watchers.close();
  }
}

class EngineTools {
  constructor(
    private readonly calls: Calls,
    /** The invocation policy and vendor keys the documents are written with, the defaults filled in. */
    readonly options: ResolvedToolOptions
  ) {}

  listBehaviors(principal: Principal): BehaviorSummary[] {
    return this.calls.sync('tools.listBehaviors', principal) as BehaviorSummary[];
  }

  describeBehavior(principal: Principal, name: string): BehaviorDocument {
    return this.calls.sync('tools.describeBehavior', principal, [name]) as BehaviorDocument;
  }

  describe(principal: Principal, name: string, target?: ToolTarget): DescribeDocument {
    const { target: plain, filter } = targetOf(target);
    return this.calls.sync('tools.describe', principal, [name, plain], filter) as DescribeDocument;
  }

  manifest(principal: Principal, target?: ToolTarget): ToolManifest {
    const { target: plain, filter } = targetOf(target);
    return this.calls.sync('tools.manifest', principal, [plain], filter) as ToolManifest;
  }

  call(principal: Principal, handle: string, args: unknown, target?: ToolTarget): unknown {
    const { target: plain, filter } = targetOf(target);
    return this.calls.sync('tools.call', principal, [handle, args, plain], filter);
  }
}

// HostedRunner runs the runner on this thread's event loop, as the
// TypeScript engine's runner ran: for an engine
// whose policy, matcher or a behavior is written in TypeScript, which
// answer on this thread alone, so the runner's own thread cannot run
// them. It wakes on a commit (setImmediate) and at the next retry,
// schedule or prune (setTimeout), and runs what is due (runner.runDue).
class HostedRunner {
  started = false;
  private soon: ReturnType<typeof setImmediate> | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private unwatch: (() => void) | undefined;

  constructor(
    private readonly calls: Calls,
    private readonly events: EngineEvents,
    private readonly clock: () => number,
    private readonly retryInitialMs: number,
    private readonly runDue: () => unknown
  ) {}

  start(): void {
    if (this.started) {
      return;
    }
    this.started = true;
    this.unwatch = this.events.watch({ committed: () => this.wake(), closed: () => this.stop() });
    this.wake();
  }

  stop(): void {
    this.started = false;
    this.unwatch?.();
    this.unwatch = undefined;
    if (this.soon !== undefined) {
      clearImmediate(this.soon);
      this.soon = undefined;
    }
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }

  private wake(): void {
    if (!this.started || this.soon !== undefined) {
      return;
    }
    this.soon = setImmediate(() => {
      this.soon = undefined;
      this.tick();
    });
  }

  private tick(): void {
    if (!this.started) {
      return;
    }
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    try {
      this.runDue();
    } catch {
      // The runner's own failure, not a reaction's: a busy file, say. It
      // tries again after the first retry delay.
      this.arm(this.clock() + this.retryInitialMs);
      return;
    }
    let due: number | null;
    try {
      due = this.calls.sync('runner.nextWake', undefined) as number | null;
    } catch {
      return;
    }
    this.arm(due ?? undefined);
  }

  private arm(at: number | undefined): void {
    if (at === undefined || !this.started) {
      return;
    }
    const delay = Math.min(Math.max(0, at - this.clock()), MAX_TIMER_MS);
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.tick();
    }, delay);
  }
}

// The longest setTimeout delay, as the TypeScript runner caps it.
const MAX_TIMER_MS = 2_147_483_647;

class EngineRunner {
  private hostedRunner: HostedRunner | undefined;

  constructor(
    private readonly calls: Calls,
    private readonly addon: AddonEngine,
    private readonly events: EngineEvents,
    private readonly runner: { principal?: unknown; retryInitialMs?: number } | undefined,
    private readonly clock: () => number,
    private readonly closed: () => boolean,
    private readonly inHook: () => boolean
  ) {}

  get running(): boolean {
    if (this.closed()) {
      return false;
    }
    return this.hostedRunner?.started === true || (this.calls.sync('runner.running', undefined) as boolean);
  }

  start(): void {
    if (!this.addon.hosted) {
      this.calls.sync('runner.start', undefined);
      return;
    }
    if (this.closed()) {
      throw new Error('the engine is closed');
    }
    if (this.runner?.principal === undefined) {
      throw new TypeError('runner.start needs a principal: open the engine with runner: { principal }');
    }
    this.hostedRunner ??= new HostedRunner(this.calls, this.events, this.clock, this.runner.retryInitialMs ?? 1000, () => this.runDue());
    this.hostedRunner.start();
  }

  stop(): void {
    if (this.hostedRunner !== undefined) {
      this.hostedRunner.stop();
    }
    this.calls.sync('runner.stop', undefined);
  }

  close(): void {
    this.hostedRunner?.stop();
    this.stop();
  }

  runDue(): RunnerPass {
    // A hook that runs the runner: the TypeScript runner's refusals.
    if (this.passing) {
      throw new Error('the runner is already running: runDue cannot run inside a reaction or a schedule');
    }
    if (this.inHook()) {
      throw new Error('the runner runs outside any transaction');
    }
    this.passing = true;
    try {
      return this.calls.sync('runner.runDue', undefined) as RunnerPass;
    } finally {
      this.passing = false;
    }
  }

  private passing = false;

  prune(): PruneResult {
    return this.calls.sync('runner.prune', undefined) as PruneResult;
  }

  status(): RunnerStatus {
    return this.calls.sync('runner.status', undefined) as RunnerStatus;
  }

  resume(key: SubscriptionKey, options?: { skip?: boolean }): SubscriptionStatus {
    return this.calls.sync('runner.resume', undefined, [key, options]) as SubscriptionStatus;
  }
}

class EngineSignals {
  constructor(private readonly calls: Calls) {}

  /**
   * send sends a signal on an instance (D77) to the event streams opened
   * with signals: its behavior declares it with access read or write, the
   * policy allows that access with the operation `signal:<name>`, its data
   * is at most 4096 bytes of JSON its paramsSchema accepts, and the
   * principal is within signals.perSecond on the instance (rate_limited).
   * Nothing is stored and no event appended.
   */
  send(principal: Principal, input: SendSignalInput): void {
    this.calls.sync('signals.send', principal, [input]);
  }
}

class EngineValues {
  constructor(private readonly calls: Calls) {}

  get(principal: Principal, hash: string, target?: ValueTarget): StoredValue {
    return this.calls.sync('values.get', principal, [hash, target]) as StoredValue;
  }

  sweep(): { removed: number } {
    return this.calls.sync('values.sweep', undefined) as { removed: number };
  }

  get thresholdBytes(): number {
    return this.calls.sync('values.thresholdBytes', undefined) as number;
  }

  get maxBytes(): number {
    return this.calls.sync('values.maxBytes', undefined) as number;
  }
}

class EngineBehaviors {
  constructor(
    private readonly calls: Calls,
    private readonly addon: AddonEngine,
    private readonly bridge: Bridge
  ) {}

  /** register registers a behavior written in TypeScript, which the test addon's bridge runs. */
  register(implementation: AnyBehaviorImplementation): void {
    if (!this.addon.bridged) {
      throw new UnsupportedError('a behavior written in TypeScript (behaviors.register)', 'bridge');
    }
    const descriptor = this.bridge.describe(implementation);
    try {
      this.addon.registerBehavior(JSON.stringify(descriptor));
    } catch (error) {
      const restored = restoreError(error, (token) => this.bridge.parkedValue(token));
      this.bridge.settle();
      throw restored;
    }
    this.bridge.settle();
  }

  has(name: string): boolean {
    return this.calls.sync('behaviors.has', undefined, [name]) as boolean;
  }

  names(): string[] {
    return this.calls.sync('behaviors.names', undefined) as string[];
  }

  declaration(name: string): BehaviorDeclaration | undefined {
    return deepFreeze(this.calls.sync('behaviors.declaration', undefined, [name]) as BehaviorDeclaration | undefined);
  }

  /**
   * invoke calls a behavior's namespace operation (D77), one its
   * declaration gives `scope: "namespace"`, which runs on the behavior's
   * tables across every schema of the namespace that composes it: not_found
   * for a behavior the engine does not run or an operation it does not
   * declare with that scope, forbidden when the policy refuses `{ principal,
   * action, namespace, behavior, operation }`, invalid_argument for
   * parameters its paramsSchema refuses. It returns the operation's result.
   */
  invoke(principal: Principal, input: NamespaceOperationInput): unknown {
    return this.calls.sync('behaviors.invoke', principal, [input]);
  }

  lookup(): never {
    throw new UnsupportedError('behaviors.lookup, which returns a TypeScript implementation', 'bridge');
  }
}

/**
 * The engine's SQLite file (`engine.storage`): raw SQL on the engine's own
 * connection, nothing checked, and
 * transactions the engine's calls inside run in savepoints of (D58: the
 * deployment's reach into the file, for its tests' looks at the tables and
 * an identity store's DDL). A store that runs transactions of its own
 * statement by statement (BEGIN IMMEDIATE, then COMMIT), as the identity
 * store does, takes a connection of its own on the file (connection()).
 */
export class EngineStorage {
  private readonly connections: Array<{ close(): void }> = [];

  constructor(
    private readonly addon: AddonEngine,
    /** The engine's file. */
    readonly path: string,
    private readonly committed: () => void
  ) {}

  /** Whether a transaction is open on the engine's connection on this thread. */
  get inTransaction(): boolean {
    return this.addon.storageInTransaction;
  }

  exec(sql: string): void {
    this.query('exec', sql, []);
  }

  run(sql: string, params: readonly SqlValue[] = []): RunResult {
    const { changes, lastInsertRowid } = this.query('run', sql, params) as { changes: number; lastInsertRowid: number | null };
    return { changes, lastInsertRowid: lastInsertRowid ?? 0 };
  }

  get(sql: string, params: readonly SqlValue[] = []): Row | undefined {
    return rowOf(this.query('get', sql, params)) as Row | undefined;
  }

  all(sql: string, params: readonly SqlValue[] = []): Row[] {
    return (this.query('all', sql, params) as unknown[]).map((row) => rowOf(row) as Row);
  }

  /** changes is how many rows the engine's connection has written since it opened (SQLite's total_changes()). */
  changes(): number {
    return Number(this.get('SELECT total_changes() AS changes')?.changes ?? 0);
  }

  /**
   * transaction runs fn in a transaction, commits when it returns and rolls
   * back when it throws; the engine's calls inside it run in savepoints of
   * it. Inside another, a savepoint of that one.
   */
  transaction<T>(fn: () => T): T {
    let outcome: { value: T } | { error: unknown } | undefined;
    try {
      this.addon.storageTransaction(() => {
        try {
          const value = fn();
          if ((typeof value === 'object' || typeof value === 'function') && value !== null && typeof (value as { then?: unknown }).then === 'function') {
            (value as unknown as PromiseLike<unknown>).then(undefined, () => undefined);
            throw new TypeError('a storage transaction is synchronous: its function returned a promise');
          }
          outcome = { value };
          return true;
        } catch (error) {
          outcome = { error };
          return false;
        }
      });
    } catch (error) {
      throw restoreError(error);
    } finally {
      this.committed();
    }
    if (outcome === undefined) {
      throw new Error('the storage transaction did not run its function');
    }
    if ('error' in outcome) {
      throw outcome.error;
    }
    return outcome.value;
  }

  /** connection opens a connection of its own on the engine's file, closed with the engine. */
  connection(): SqliteConnection {
    const connection = openConnection(this.path);
    this.connections.push(connection);
    return connection;
  }

  /** close closes the connections connection() opened. */
  close(): void {
    for (const connection of this.connections.splice(0)) {
      try {
        connection.close();
      } catch {
        // Closed already.
      }
    }
  }

  private query(mode: 'get' | 'all' | 'run' | 'exec', sql: string, params: readonly SqlValue[]): unknown {
    let text: string;
    try {
      text = this.addon.storageQuery(mode, String(sql), JSON.stringify(sqlParams(params)));
    } catch (error) {
      throw restoreError(error);
    }
    return JSON.parse(text);
  }
}

/** One route the engine serves, as api::routes lists it. */
export interface EngineRoute {
  readonly name: string;
  readonly method: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  readonly path: string;
  /** Whether its response may be an event stream. */
  readonly stream: boolean;
  /** Whether it is the MCP endpoint's. */
  readonly mcp: boolean;
}

/** The engine: its API over the Rust engine's addon, opened by openEngine. */
export class Engine {
  readonly namespaces: EngineNamespaces;
  readonly behaviors: EngineBehaviors;
  readonly schemas: EngineSchemas;
  readonly instances: EngineInstances;
  readonly events: EngineEvents;
  readonly tools: EngineTools;
  readonly runner: EngineRunner;
  readonly values: EngineValues;
  /** The signals of instances (D77): what a caller sends to the event streams open on one. */
  readonly signals: EngineSignals;
  /** Decides whether a principal holds a permission, as the options gave it; hasAnyPermission by default. */
  readonly permissionMatcher: PermissionMatcher;
  /** `sqlite`, or `postgres`, whose engine is called through callAsync. */
  readonly dialect: 'sqlite' | 'postgres';
  private readonly addon: AddonEngine;
  // The addon module that opened it, whose routes it serves.
  private readonly module: Addon;
  private readonly calls: Calls;
  private readonly watchers: Watchers;
  private readonly nativeStorage: EngineStorage | undefined;

  private constructor(
    module: Addon,
    addon: AddonEngine,
    clock: (() => number) | undefined,
    hosts: Hosts,
    permissionMatcher: PermissionMatcher,
    tools: ResolvedToolOptions,
    runner: { principal?: unknown; retryInitialMs?: number } | undefined,
    path: string | undefined
  ) {
    this.module = module;
    this.addon = addon;
    this.dialect = addon.dialect;
    this.permissionMatcher = permissionMatcher;
    const watchers = new Watchers(addon);
    this.watchers = watchers;
    this.calls = new Calls(addon, clock, hosts, watchers);
    watchers.attach(this.calls);
    this.namespaces = new EngineNamespaces(this.calls);
    this.behaviors = new EngineBehaviors(this.calls, addon, hosts.bridge);
    this.schemas = new EngineSchemas(this.calls);
    this.instances = new EngineInstances(this.calls);
    this.events = new EngineEvents(this.calls, watchers);
    this.tools = new EngineTools(this.calls, tools);
    this.runner = new EngineRunner(this.calls, addon, this.events, runner, clock ?? Date.now, () => watchers.closed, () => hosts.bridge.depth > 0);
    this.values = new EngineValues(this.calls);
    this.signals = new EngineSignals(this.calls);
    this.nativeStorage = path === undefined || addon.dialect !== 'sqlite' ? undefined : new EngineStorage(addon, path, () => watchers.poll());
  }

  /** open opens the engine on the options: its storage migrated, the core's behaviors and the ones the options name registered. */
  static open(options: EngineOptions): Engine {
    if (options.policy === undefined || options.policy === null) {
      throw new TypeError('an engine needs an access policy (policy); pass allowAll for tests and local use');
    }
    const { json, clock, hosted } = nativeOptions(options);
    // The tool options with their defaults filled in, as the HTTP and MCP
    // adapters read them (engine.tools.options), resolved before the file
    // opens as the TypeScript engine resolves them: the Rust engine checks
    // the same, but a value JSON cannot carry (NaN) reaches it as another.
    const tools = resolveToolOptions(options.tools);
    const bridge = new Bridge();
    const hosts = new Hosts(hosted, bridge);
    const loaded = loadAddon(options.addon);
    if (loaded.bridgeCall !== undefined) {
      bridge.connect(loaded.bridgeCall);
    }
    bridge.restore = (error) => restoreError(error, (token) => bridge.parkedValue(token));
    if (hosted.behaviors !== undefined) {
      if (loaded.bridgeCall === undefined) {
        throw new UnsupportedError('a behavior written in TypeScript (defineBehavior), which only the test addon runs', 'bridge');
      }
      json.jsBehaviors = hosted.behaviors.map((implementation) => bridge.describe(implementation));
    }
    let addon: AddonEngine;
    try {
      addon = loaded.NativeEngine.open(JSON.stringify(json), hosts.answer);
    } catch (error) {
      const restored = restoreError(error, (token) => bridge.parkedValue(token));
      bridge.settle();
      hosts.rethrow();
      throw restored;
    }
    bridge.settle();
    try {
      hosts.rethrow();
    } catch (error) {
      addon.close();
      throw error;
    }
    return new Engine(loaded, addon, clock, hosts, options.permissionMatcher ?? hasAnyPermission, tools, options.runner, options.path);
  }

  /** The engine's SQLite file (EngineStorage); an engine on Postgres has none. */
  get storage(): EngineStorage {
    if (this.nativeStorage === undefined) {
      throw new UnsupportedError('engine.storage on Postgres, which is the SQLite file\'s', 'storage');
    }
    return this.nativeStorage;
  }

  /** The routes the engine serves (api::routes): what engineApp and engineMcp mount. */
  routes(): EngineRoute[] {
    return JSON.parse(this.module.engineRoutes()) as EngineRoute[];
  }

  /** handleHttp serves one request through the engine's own routes (src/native/http.ts). */
  handleHttp(head: Record<string, unknown>, body: Buffer | undefined, filter?: ToolFilter): { answer: HttpAnswer; thrown?: { value: unknown } } {
    return this.calls.http(head, body, filter);
  }

  /** streamPull pulls an event stream handleHttp holds open, without waiting. */
  streamPull(id: number): StreamPull {
    try {
      return this.addon.streamPull(id);
    } catch (error) {
      throw restoreError(error);
    }
  }

  /**
   * onWake calls wake each time a commit, or a signal an event stream
   * keeps (D77), moves the engine's notifier, on this thread: what an event
   * stream's body waits on (src/native/http.ts).
   */
  onWake(wake: () => void): () => void {
    return this.watchers.onWake(wake);
  }

  /** streamClose ends an event stream handleHttp holds open. */
  streamClose(id: number): void {
    this.addon.streamClose(id);
  }

  /** search searches every schema the namespace reaches that composes Search, as Engine.search does. */
  search(principal: Principal, params: unknown, target?: { namespace?: string }): Page<SchemaSearchHit> {
    return this.calls.sync('search', principal, [params, target]) as Page<SchemaSearchHit>;
  }

  /**
   * callAsync makes a call by name off this thread and resolves with its
   * result: the way an engine on Postgres is called until openEngineAsync.
   */
  callAsync(op: string, principal: Principal | undefined, ...args: unknown[]): Promise<unknown> {
    return this.calls.async(op, principal, args);
  }

  /** shutdown stops the runner and ends every event stream; every later call is refused. */
  shutdown(): void {
    try {
      this.runner.close();
    } catch {
      // Shut down already.
    }
    this.watchers.close();
    this.addon.shutdown();
  }

  /** close shuts the engine down and lets it go; its storage closes once a call in flight returns. */
  close(): void {
    try {
      this.runner.close();
    } catch {
      // Closed already.
    }
    this.watchers.close();
    this.nativeStorage?.close();
    this.addon.close();
  }
}

/**
 * openEngine opens the engine (D16, D58) on a SQLite file (options.path),
 * its calls synchronous on this thread; an engine on Postgres is
 * openEngineAsync's.
 */
export function openEngine(options: EngineOptions): Engine {
  return Engine.open(options);
}
