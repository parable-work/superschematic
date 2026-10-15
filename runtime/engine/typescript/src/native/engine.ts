/*
The engine's synchronous, principal-first API over the Rust engine (D58):
each method writes its call by name with JSON arguments to the addon's
callSync, which makes it on this thread, and reads its result back, or
rethrows its error as the TypeScript engine's class. It is the
TypeScript Engine's shape, so the engine's suite runs against it
(ENGINE_IMPL=rust, test/helpers.ts); what it does not do yet throws
NativeUnsupportedError, naming the gap (test/rust-pending.txt).

On Postgres a synchronous call would hold this thread while it waits on
the database, so the addon refuses it (sync_on_async_storage): an engine
on Postgres is called through callAsync, until openEngineAsync gives it
this API with promises (milestone 7b).
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';

import type { AccessRequest, Principal } from '../access.js';
import type { SchemaSearchHit } from '../behaviors/core/index.js';
import type { BehaviorDeclaration } from '../behaviors/declaration.js';
import type { Page } from '../behaviors/paging.js';
import type { EventPage, ReadEventsOptions } from '../events/log.js';
import type { EventWatcher } from '../events/notifier.js';
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
} from '../instances/store.js';
import type { NamespaceRecord } from '../namespaces.js';
import type { PublishResult, SchemaRecord, SchemaSummary } from '../registry/catalog.js';
import type { ComposedBehavior, DefineOptions, SchemaCapabilities, SchemaTarget, ValidateOptions } from '../registry/registry.js';
import { EngineError, type ValidationIssue } from '../errors.js';
import { DEFAULT_NAMESPACE } from '../namespaces.js';
import type { PruneResult, RunnerPass, RunnerStatus, SubscriptionKey, SubscriptionStatus } from '../runner/runner.js';
import type { BehaviorDocument, BehaviorSummary } from '../tools/behaviors.js';
import type { DescribeDocument, ToolManifest, ToolTarget } from '../tools/catalog.js';
import { resolveToolOptions, type ResolvedToolOptions } from '../tools/options.js';
import type { StoredValue } from '../values/store.js';
import type { ValueTarget } from '../values/values.js';
import type { AnyBehaviorImplementation } from '../behaviors/behavior.js';
import { deepFreeze } from '../behaviors/json.js';
import { loadAddon, type AddonEngine } from './addon.js';
import { Bridge } from './bridge.js';
import { NativeUnsupportedError, restoreError } from './errors.js';
import { nativeOptions, type HostedOptions, type NativeEngineOptions } from './options.js';

// Hosts answers what the options leave to TypeScript (a policy, a
// permission matcher), which the addon asks on this thread during a
// synchronous call: host(kind, json) -> json. It never throws into the
// addon: it keeps the first error the TypeScript code threw, and the call
// rethrows it once the addon returns, as the TypeScript engine would have
// thrown it from inside the call. A policy's answer is checked as the
// TypeScript engine's Access checks it.
class Hosts {
  private parked: { error: unknown } | undefined;

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
      return 'false';
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

// Calls makes the engine's calls by name: { op, principal, args, now }.
class Calls {
  constructor(
    private readonly addon: AddonEngine,
    private readonly clock: (() => number) | undefined,
    private readonly hosts: Hosts,
    private readonly watchers: Watchers
  ) {}

  sync(op: string, principal: Principal | undefined, args: readonly unknown[] = []): unknown {
    let text: string;
    const bridge = this.hosts.bridge;
    try {
      text = this.addon.callSync(this.request(op, principal, args));
    } catch (error) {
      const restored = restoreError(error, (token) => bridge.parkedValue(token));
      bridge.settle();
      this.watchers.poll();
      this.hosts.rethrow();
      throw restored;
    }
    bridge.settle();
    this.watchers.poll();
    this.hosts.rethrow();
    return text === '' ? undefined : JSON.parse(text);
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

  private request(op: string, principal: Principal | undefined, args: readonly unknown[]): string {
    // A trailing argument left out is one the call defaults.
    let end = args.length;
    while (end > 0 && args[end - 1] === undefined) {
      end -= 1;
    }
    return JSON.stringify({ op, principal, args: args.slice(0, end), now: this.clock?.() });
  }
}

// target checks a tool target: a filter is a function, which JSON cannot
// carry; a mount's tool filter comes with the HTTP adapters (7b).
function checkTarget(target: ToolTarget | undefined): ToolTarget | undefined {
  if (target?.filter !== undefined) {
    throw new NativeUnsupportedError('a tool filter written in TypeScript (ToolTarget.filter)', 'http');
  }
  return target;
}

class NativeNamespaces {
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

class NativeSchemas {
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

  validator(): never {
    throw new NativeUnsupportedError('schemas.validator, which returns a TypeScript SchemaValidator', 'validator');
  }

  behaviors(principal: Principal, name: string, options?: ValidateOptions): ComposedBehavior[] {
    return this.calls.sync('schemas.behaviors', principal, [name, options]) as ComposedBehavior[];
  }
}

class NativeInstances {
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
// engine's notifier tells them (src/events/notifier.ts): after each commit
// that appended events, once per event, with its cursor. A call this
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
    if (!this.watching) {
      this.watching = true;
      this.addon.watch(() => this.poll());
    }
    return () => {
      this.watchers.delete(watcher);
    };
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
    for (const watcher of watchers) {
      try {
        watcher.closed?.();
      } catch {
        // As in poll.
      }
    }
  }
}

class NativeEvents {
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

class NativeTools {
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
    return this.calls.sync('tools.describe', principal, [name, checkTarget(target)]) as DescribeDocument;
  }

  manifest(principal: Principal, target?: ToolTarget): ToolManifest {
    return this.calls.sync('tools.manifest', principal, [checkTarget(target)]) as ToolManifest;
  }

  call(principal: Principal, handle: string, args: unknown, target?: ToolTarget): unknown {
    return this.calls.sync('tools.call', principal, [handle, args, checkTarget(target)]);
  }
}

// HostedRunner runs the runner on this thread's event loop, as the
// TypeScript engine's runner runs (src/runner/runner.ts): for an engine
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
    private readonly events: NativeEvents,
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

class NativeRunner {
  private hostedRunner: HostedRunner | undefined;

  constructor(
    private readonly calls: Calls,
    private readonly addon: AddonEngine,
    private readonly events: NativeEvents,
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

class NativeValues {
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

class NativeBehaviors {
  constructor(
    private readonly calls: Calls,
    private readonly addon: AddonEngine,
    private readonly bridge: Bridge
  ) {}

  /** register registers a behavior written in TypeScript, which the test addon's bridge runs. */
  register(implementation: AnyBehaviorImplementation): void {
    if (!this.addon.bridged) {
      throw new NativeUnsupportedError('a behavior written in TypeScript (behaviors.register)', 'bridge');
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

  lookup(): never {
    throw new NativeUnsupportedError('behaviors.lookup, which returns a TypeScript implementation', 'bridge');
  }
}

/** The engine on the Rust engine: the TypeScript Engine's API over the addon. */
export class NativeEngine {
  readonly namespaces: NativeNamespaces;
  readonly behaviors: NativeBehaviors;
  readonly schemas: NativeSchemas;
  readonly instances: NativeInstances;
  readonly events: NativeEvents;
  readonly tools: NativeTools;
  readonly runner: NativeRunner;
  readonly values: NativeValues;
  /** Decides whether a principal holds a permission, as the options gave it; hasAnyPermission by default. */
  readonly permissionMatcher: PermissionMatcher;
  /** `sqlite`, or `postgres`, whose engine is called through callAsync. */
  readonly dialect: 'sqlite' | 'postgres';
  private readonly addon: AddonEngine;
  private readonly calls: Calls;
  private readonly watchers: Watchers;

  private constructor(
    addon: AddonEngine,
    clock: (() => number) | undefined,
    hosts: Hosts,
    permissionMatcher: PermissionMatcher,
    tools: ResolvedToolOptions,
    runner: { principal?: unknown; retryInitialMs?: number } | undefined
  ) {
    this.addon = addon;
    this.dialect = addon.dialect;
    this.permissionMatcher = permissionMatcher;
    const watchers = new Watchers(addon);
    this.watchers = watchers;
    this.calls = new Calls(addon, clock, hosts, watchers);
    watchers.attach(this.calls);
    this.namespaces = new NativeNamespaces(this.calls);
    this.behaviors = new NativeBehaviors(this.calls, addon, hosts.bridge);
    this.schemas = new NativeSchemas(this.calls);
    this.instances = new NativeInstances(this.calls);
    this.events = new NativeEvents(this.calls, watchers);
    this.tools = new NativeTools(this.calls, tools);
    this.runner = new NativeRunner(this.calls, addon, this.events, runner, clock ?? Date.now, () => watchers.closed, () => hosts.bridge.depth > 0);
    this.values = new NativeValues(this.calls);
  }

  /** open opens the Rust engine on the options, as Engine.open opens the TypeScript one. */
  static open(options: NativeEngineOptions): NativeEngine {
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
    const loaded = loadAddon();
    if (loaded.bridgeCall !== undefined) {
      bridge.connect(loaded.bridgeCall);
    }
    bridge.restore = (error) => restoreError(error, (token) => bridge.parkedValue(token));
    if (hosted.behaviors !== undefined) {
      if (loaded.bridgeCall === undefined) {
        throw new NativeUnsupportedError('a behavior written in TypeScript (defineBehavior), which only the test addon runs', 'bridge');
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
    return new NativeEngine(addon, clock, hosts, options.permissionMatcher ?? hasAnyPermission, tools, options.runner);
  }

  /** The TypeScript engine's storage, which the Rust engine does not expose (milestone 7b decides how). */
  get storage(): never {
    throw new NativeUnsupportedError('engine.storage', 'storage');
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
    this.addon.close();
  }
}

/** openNativeEngine is NativeEngine.open. */
export function openNativeEngine(options: NativeEngineOptions): NativeEngine {
  return NativeEngine.open(options);
}
