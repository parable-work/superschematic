/*
The engine: one SQLite file, brought up to the engine's migrations when it
opens, the namespaces the deployment configures, and the schema registry.
*/

import { SchemaFileLoader } from '@superschematic/schema-runtime';

import { engineMigrations } from './migrations.js';
import { Namespaces, type NamespaceOptions } from './namespaces.js';
import { SchemaRegistry } from './registry/registry.js';
import { migrate } from './storage/migrations.js';
import { Storage, type StorageOptions } from './storage/storage.js';

export interface EngineOptions extends StorageOptions {
  /** The SQLite file. One process writes it. */
  path: string;
  /**
   * The meta-schema schema documents load against: the `superschematic
   * json-schema` output of the deployment's binary, parsed or as JSON
   * text. The default is the core registry's.
   */
  metaSchema?: Record<string, unknown> | string;
  namespaces?: NamespaceOptions;
  /** The time in epoch milliseconds; Date.now by default. */
  clock?: () => number;
}

export class Engine {
  readonly storage: Storage;
  readonly namespaces: Namespaces;
  readonly schemas: SchemaRegistry;

  private constructor(storage: Storage, namespaces: Namespaces, schemas: SchemaRegistry) {
    this.storage = storage;
    this.namespaces = namespaces;
    this.schemas = schemas;
  }

  /** open opens the engine's file, creating it if absent, and applies the engine's migrations. */
  static open(options: EngineOptions): Engine {
    const namespaces = new Namespaces(options.namespaces);
    const loader = new SchemaFileLoader({ metaSchema: options.metaSchema });
    const clock = options.clock ?? Date.now;
    const storage = Storage.open(options.path, options);
    try {
      migrate(storage, engineMigrations, clock());
    } catch (error) {
      storage.close();
      throw error;
    }
    return new Engine(storage, namespaces, new SchemaRegistry(storage, namespaces, loader, clock));
  }

  close(): void {
    this.storage.close();
  }
}

/** openEngine is Engine.open. */
export function openEngine(options: EngineOptions): Engine {
  return Engine.open(options);
}
