/*
The engine: one SQLite file, brought up to the engine's migrations when it
opens, the namespaces the deployment configures, the access policy it
supplies, the core's behaviors and the implementations it registers, the
schema registry, instance store and event log, each of which asks that
policy on every call, the tool catalog, which reads and calls through
them, and the runner, which runs the behaviors' reactions and schedules
after the commit as the principal the deployment names for it.
*/

import { hasAnyPermission, type PermissionMatcher } from '@superschematic/http-runtime';
import { SchemaFileLoader } from '@superschematic/schema-runtime';

import { Access, type AccessPolicy } from './access.js';
import type { AnyBehaviorImplementation } from './behaviors/behavior.js';
import { coreBehaviors } from './behaviors/core/index.js';
import { BehaviorRegistry } from './behaviors/registry.js';
import { EventLog } from './events/log.js';
import { InstanceStore, defaultIds } from './instances/store.js';
import { engineMigrations } from './migrations.js';
import { Namespaces, type NamespaceOptions } from './namespaces.js';
import { SchemaCatalog } from './registry/catalog.js';
import { SchemaRegistry } from './registry/registry.js';
import { Runner, type RunnerOptions } from './runner/runner.js';
import { migrate } from './storage/migrations.js';
import { Storage, type StorageOptions } from './storage/storage.js';
import { ToolCatalog } from './tools/catalog.js';
import { resolveToolOptions, type ToolOptions } from './tools/options.js';

export interface EngineOptions extends StorageOptions {
  /** The SQLite file. One process writes it. */
  path: string;
  /**
   * Who may read, write, define and publish. There is no default: pass
   * the deployment's policy, or allowAll for tests and local use.
   */
  policy: AccessPolicy;
  /**
   * The meta-schema schema documents load against: the `superschematic
   * json-schema` output of the deployment's binary, parsed or as JSON
   * text. The default is the core registry's.
   */
  metaSchema?: Record<string, unknown> | string;
  namespaces?: NamespaceOptions;
  /** Makes the id of an instance created without one; random UUIDs by default. */
  ids?: () => string;
  /** The time in epoch milliseconds; Date.now by default. */
  clock?: () => number;
  /**
   * The behavior implementations this engine runs besides the core's
   * (Workflow, Comments, Revisions, Dependencies, Links, Rollups and
   * Search, which it registers first), registered when it opens;
   * engine.behaviors.register adds more later. A schema that composes a
   * behavior without one is refused.
   */
  behaviors?: readonly AnyBehaviorImplementation[];
  /**
   * The invocation policy and the vendor-extension keys the describe and
   * tools documents and the MCP tools are written with: the deployment's
   * binary's registrations, the core's by default (runtime/engine/README.md,
   * "Tools").
   */
  tools?: ToolOptions;
  /**
   * Decides whether a principal holds a permission a behavior's config
   * names (a behavior's can()). The default is the HTTP runtime's
   * hasAnyPermission: dotted paths, where a granted permission covers
   * itself and every permission nested under it, and no root permission.
   * A deployment with its own vocabulary passes the matcher it gives the
   * HTTP runtime.
   */
  permissionMatcher?: PermissionMatcher;
  /**
   * The runner of behaviors' reactions and schedules (D16, amended): the
   * principal they act as, which the access policy is asked about like
   * any caller, and its retry and depth limits. Without it the runner
   * refuses to start; engine.runner.start() starts it.
   */
  runner?: RunnerOptions;
}

export class Engine {
  readonly storage: Storage;
  readonly namespaces: Namespaces;
  readonly behaviors: BehaviorRegistry;
  readonly schemas: SchemaRegistry;
  readonly instances: InstanceStore;
  readonly events: EventLog;
  readonly tools: ToolCatalog;
  /** Runs reactions and schedules after the commit; the deployment starts and stops it. */
  readonly runner: Runner;

  private constructor(
    storage: Storage,
    namespaces: Namespaces,
    behaviors: BehaviorRegistry,
    schemas: SchemaRegistry,
    instances: InstanceStore,
    events: EventLog,
    tools: ToolCatalog,
    runner: Runner
  ) {
    this.storage = storage;
    this.namespaces = namespaces;
    this.behaviors = behaviors;
    this.schemas = schemas;
    this.instances = instances;
    this.events = events;
    this.tools = tools;
    this.runner = runner;
  }

  /** open opens the engine's file, creating it if absent, and applies the engine's migrations. */
  static open(options: EngineOptions): Engine {
    const access = new Access(options.policy);
    const namespaces = new Namespaces(options.namespaces);
    const tools = resolveToolOptions(options.tools);
    const loader = new SchemaFileLoader({ metaSchema: options.metaSchema });
    const clock = options.clock ?? Date.now;
    const permissionMatcher = options.permissionMatcher ?? hasAnyPermission;
    if (typeof permissionMatcher !== 'function') {
      throw new TypeError('permissionMatcher is a function (held, required) => boolean');
    }
    // Checked before the file opens, so a bad option leaves nothing open.
    const runnerOptions = options.runner;
    Runner.check(runnerOptions);
    const storage = Storage.open(options.path, options);
    const behaviors = new BehaviorRegistry(storage, clock, tools.invocationPolicy);
    try {
      migrate(storage, engineMigrations, clock());
      for (const implementation of [...coreBehaviors, ...(options.behaviors ?? [])]) {
        behaviors.register(implementation);
      }
    } catch (error) {
      storage.close();
      throw error;
    }
    const catalog = new SchemaCatalog(storage, namespaces, loader, behaviors, clock);
    const schemas = new SchemaRegistry(catalog, namespaces, access);
    const instances = new InstanceStore(storage, namespaces, catalog, access, options.ids ?? defaultIds, clock, permissionMatcher);
    const events = new EventLog(storage, namespaces, access);
    return new Engine(
      storage,
      namespaces,
      behaviors,
      schemas,
      instances,
      events,
      new ToolCatalog(namespaces, access, schemas, instances, tools),
      new Runner(storage, namespaces, catalog, behaviors, instances.reach, events, clock, permissionMatcher, runnerOptions)
    );
  }

  /** close stops the runner, ends the engine's event watchers, then closes its file. */
  close(): void {
    this.runner.close();
    this.events.close();
    this.storage.close();
  }
}

/** openEngine is Engine.open. */
export function openEngine(options: EngineOptions): Engine {
  return Engine.open(options);
}
