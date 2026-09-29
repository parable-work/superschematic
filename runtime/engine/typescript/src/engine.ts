/*
The engine: one SQLite file, brought up to the engine's migrations when it
opens, the namespaces the deployment configures, the access policy it
supplies, the behavior implementations it registers, and the schema
registry, instance store and event log, each of which asks that policy on
every call.
*/

import { SchemaFileLoader } from '@superschematic/schema-runtime';

import { Access, type AccessPolicy } from './access.js';
import type { AnyBehaviorImplementation } from './behaviors/behavior.js';
import { BehaviorRegistry } from './behaviors/registry.js';
import { EventLog } from './events/log.js';
import { InstanceStore, defaultIds } from './instances/store.js';
import { engineMigrations } from './migrations.js';
import { Namespaces, type NamespaceOptions } from './namespaces.js';
import { SchemaCatalog } from './registry/catalog.js';
import { SchemaRegistry } from './registry/registry.js';
import { migrate } from './storage/migrations.js';
import { Storage, type StorageOptions } from './storage/storage.js';

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
   * The behavior implementations this engine runs, registered when it
   * opens; engine.behaviors.register adds more later. A schema that
   * composes a behavior without one is refused.
   */
  behaviors?: readonly AnyBehaviorImplementation[];
}

export class Engine {
  readonly storage: Storage;
  readonly namespaces: Namespaces;
  readonly behaviors: BehaviorRegistry;
  readonly schemas: SchemaRegistry;
  readonly instances: InstanceStore;
  readonly events: EventLog;

  private constructor(
    storage: Storage,
    namespaces: Namespaces,
    behaviors: BehaviorRegistry,
    schemas: SchemaRegistry,
    instances: InstanceStore,
    events: EventLog
  ) {
    this.storage = storage;
    this.namespaces = namespaces;
    this.behaviors = behaviors;
    this.schemas = schemas;
    this.instances = instances;
    this.events = events;
  }

  /** open opens the engine's file, creating it if absent, and applies the engine's migrations. */
  static open(options: EngineOptions): Engine {
    const access = new Access(options.policy);
    const namespaces = new Namespaces(options.namespaces);
    const loader = new SchemaFileLoader({ metaSchema: options.metaSchema });
    const clock = options.clock ?? Date.now;
    const storage = Storage.open(options.path, options);
    const behaviors = new BehaviorRegistry(storage, clock);
    try {
      migrate(storage, engineMigrations, clock());
      for (const implementation of options.behaviors ?? []) {
        behaviors.register(implementation);
      }
    } catch (error) {
      storage.close();
      throw error;
    }
    const catalog = new SchemaCatalog(storage, namespaces, loader, behaviors, clock);
    return new Engine(
      storage,
      namespaces,
      behaviors,
      new SchemaRegistry(catalog, namespaces, access),
      new InstanceStore(storage, namespaces, catalog, access, options.ids ?? defaultIds, clock),
      new EventLog(storage, namespaces, access)
    );
  }

  /** close ends the engine's event watchers, then closes its file. */
  close(): void {
    this.events.close();
    this.storage.close();
  }
}

/** openEngine is Engine.open. */
export function openEngine(options: EngineOptions): Engine {
  return Engine.open(options);
}
