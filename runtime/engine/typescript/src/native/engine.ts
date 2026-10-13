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

import type { Principal } from '../access.js';
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
import type { ValidationIssue } from '../errors.js';
import type { PruneResult, RunnerPass, RunnerStatus, SubscriptionKey, SubscriptionStatus } from '../runner/runner.js';
import type { BehaviorDocument, BehaviorSummary } from '../tools/behaviors.js';
import type { DescribeDocument, ToolManifest, ToolTarget } from '../tools/catalog.js';
import type { StoredValue } from '../values/store.js';
import type { ValueTarget } from '../values/values.js';
import { loadAddon, type AddonEngine } from './addon.js';
import { NativeUnsupportedError, restoreError } from './errors.js';
import { nativeOptions, type NativeEngineOptions } from './options.js';

// Calls makes the engine's calls by name: { op, principal, args, now }.
class Calls {
  constructor(
    private readonly addon: AddonEngine,
    private readonly clock: (() => number) | undefined
  ) {}

  sync(op: string, principal: Principal | undefined, args: readonly unknown[] = []): unknown {
    let text: string;
    try {
      text = this.addon.callSync(this.request(op, principal, args));
    } catch (error) {
      throw restoreError(error);
    }
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
    throw new NativeUnsupportedError('a tool filter written in TypeScript (ToolTarget.filter)', 'bridge');
  }
  return target;
}

class NativeNamespaces {
  constructor(private readonly calls: Calls) {}

  get names(): readonly string[] {
    return this.calls.sync('namespaces.names', undefined) as string[];
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
    throw new NativeUnsupportedError('schemas.validator, which returns a TypeScript SchemaValidator', 'bridge');
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

class NativeEvents {
  constructor(private readonly calls: Calls) {}

  read(principal: Principal, options?: ReadEventsOptions): EventPage {
    return this.calls.sync('events.read', principal, [options]) as EventPage;
  }

  head(): number {
    return this.calls.sync('events.head', undefined) as number;
  }

  floor(namespace?: string): number {
    return this.calls.sync('events.floor', undefined, [namespace]) as number;
  }

  watch(_watcher: EventWatcher): () => void {
    throw new NativeUnsupportedError('events.watch', 'watch');
  }

  get watching(): number {
    return 0;
  }

  close(): void {}
}

class NativeTools {
  constructor(private readonly calls: Calls) {}

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

class NativeRunner {
  constructor(private readonly calls: Calls) {}

  get running(): boolean {
    return this.calls.sync('runner.running', undefined) as boolean;
  }

  start(): void {
    this.calls.sync('runner.start', undefined);
  }

  stop(): void {
    this.calls.sync('runner.stop', undefined);
  }

  close(): void {
    this.stop();
  }

  runDue(): RunnerPass {
    return this.calls.sync('runner.runDue', undefined) as RunnerPass;
  }

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

  get thresholdBytes(): number {
    return this.calls.sync('values.thresholdBytes', undefined) as number;
  }

  get maxBytes(): number {
    return this.calls.sync('values.maxBytes', undefined) as number;
  }
}

class NativeBehaviors {
  constructor(private readonly calls: Calls) {}

  register(): never {
    throw new NativeUnsupportedError('a behavior written in TypeScript (behaviors.register)', 'bridge');
  }

  has(name: string): boolean {
    return this.calls.sync('behaviors.has', undefined, [name]) as boolean;
  }

  names(): string[] {
    return this.calls.sync('behaviors.names', undefined) as string[];
  }

  declaration(name: string): BehaviorDeclaration | undefined {
    return this.calls.sync('behaviors.declaration', undefined, [name]) as BehaviorDeclaration | undefined;
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
  readonly permissionMatcher: PermissionMatcher = hasAnyPermission;
  /** `sqlite`, or `postgres`, whose engine is called through callAsync. */
  readonly dialect: 'sqlite' | 'postgres';
  private readonly addon: AddonEngine;
  private readonly calls: Calls;

  private constructor(addon: AddonEngine, clock: (() => number) | undefined) {
    this.addon = addon;
    this.dialect = addon.dialect;
    this.calls = new Calls(addon, clock);
    this.namespaces = new NativeNamespaces(this.calls);
    this.behaviors = new NativeBehaviors(this.calls);
    this.schemas = new NativeSchemas(this.calls);
    this.instances = new NativeInstances(this.calls);
    this.events = new NativeEvents(this.calls);
    this.tools = new NativeTools(this.calls);
    this.runner = new NativeRunner(this.calls);
    this.values = new NativeValues(this.calls);
  }

  /** open opens the Rust engine on the options, as Engine.open opens the TypeScript one. */
  static open(options: NativeEngineOptions): NativeEngine {
    const { json, clock } = nativeOptions(options);
    let addon: AddonEngine;
    try {
      addon = loadAddon().NativeEngine.open(JSON.stringify(json));
    } catch (error) {
      throw restoreError(error);
    }
    return new NativeEngine(addon, clock);
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
    this.addon.shutdown();
  }

  /** close shuts the engine down and lets it go; its storage closes once a call in flight returns. */
  close(): void {
    this.addon.close();
  }
}

/** openNativeEngine is NativeEngine.open. */
export function openNativeEngine(options: NativeEngineOptions): NativeEngine {
  return NativeEngine.open(options);
}
