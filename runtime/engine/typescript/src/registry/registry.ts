/*
The schema registry: a schema name has one draft and a line of published
versions in each namespace. define stores the draft, replacing the one
before it; publish makes the draft the next live version (1, 2, 3, ...).
Instances are read and written with the live version, the newest one.

Both refuse a document the engine does not take (document.ts) and a
version the compatibility rule refuses against the live one (compat.ts).
publish loads the draft again, so it also meets the deployment's current
meta-schema. A draft identical to the live version (the same canonical
form, so the same hash) publishes nothing: no version is minted and the
draft is dropped. There is no deprecate or promote: a live version is
replaced only by publishing a newer one, so an older version never comes
back into use.

The validator of a version is built once and cached per namespace, name
and version; a version never changes after it is published.
*/

import { createHash } from 'node:crypto';

import type { SchemaFileLoader } from '@superschematic/schema-runtime';
import type { Document } from '@superschematic/schema-ir/schema-file';

import { EngineError, IncompatibleChangeError } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { incompatibleChanges } from './compat.js';
import { modelOf, readSchema, SCHEMA_NAME, type SchemaModel } from './document.js';
import { SchemaValidator, type ValidationIssue } from './validator.js';

/** A stored draft or published version of a schema. */
export interface SchemaRecord {
  /** The namespace that holds the schema. */
  namespace: string;
  name: string;
  /** The published version, or null for the draft. */
  version: number | null;
  /** The type that holds instances. */
  instanceType: string;
  document: Document;
  /** The document's canonical JSON, as stored. */
  canonical: string;
  /** The SHA-256 of canonical, hex. */
  hash: string;
  /** When this document was defined, in epoch milliseconds. */
  definedAt: number;
  /** When it was published; null for the draft. */
  publishedAt: number | null;
}

/** A schema name a namespace reaches. */
export interface SchemaSummary {
  namespace: string;
  name: string;
  /** The live version, or null when only a draft exists. */
  liveVersion: number | null;
  hasDraft: boolean;
}

/** The outcome of publish. */
export interface PublishResult {
  namespace: string;
  name: string;
  /** The live version after the call. */
  version: number;
  /** False when the draft matched the live version and nothing was minted. */
  published: boolean;
}

/** Where a call looks: a namespace, `default` when absent. */
export interface SchemaTarget {
  namespace?: string;
}

export interface DefineOptions extends SchemaTarget {
  /** Names the document in errors; `schema` when absent. */
  source?: string;
}

export interface ValidateOptions extends SchemaTarget {
  /** A published version to validate against; the live one when absent. */
  version?: number;
}

const DRAFT = 0;

const COLUMNS = 'namespace, name, version, document, hash, defined_at, published_at';

export class SchemaRegistry {
  private readonly validators = new Map<string, SchemaValidator>();

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly loader: SchemaFileLoader,
    private readonly clock: () => number
  ) {}

  /**
   * define stores a schema document as the draft of its name in a
   * namespace, replacing the draft before it. input is the document's JSON
   * text, or the document as a value.
   */
  define(input: string | Record<string, unknown>, options: DefineOptions = {}): SchemaRecord {
    const namespace = this.namespaces.resolve(options.namespace);
    const text = typeof input === 'string' ? input : JSON.stringify(input);
    const model = readSchema(this.loader, text, options.source ?? 'schema');
    const now = this.clock();
    return this.storage.transaction(() => {
      this.checkNameSide(namespace, model.name);
      const live = this.row(namespace, model.name, 'live');
      if (live) {
        this.checkCompatible(namespace, live, model);
      }
      this.storage.run(
        `INSERT INTO engine_schemas (${COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, NULL)
         ON CONFLICT (namespace, name, version) DO UPDATE
         SET document = excluded.document, hash = excluded.hash, defined_at = excluded.defined_at`,
        [namespace, model.name, DRAFT, model.canonical, hashOf(model.canonical), now]
      );
      return toRecord(this.row(namespace, model.name, DRAFT) as Row);
    });
  }

  /** publish makes the draft of a name the next live version. */
  publish(name: string, options: SchemaTarget = {}): PublishResult {
    const namespace = this.namespaces.resolve(options.namespace);
    checkName(name);
    const now = this.clock();
    return this.storage.transaction(() => {
      const draft = this.row(namespace, name, DRAFT);
      if (!draft) {
        throw new EngineError('not_found', `schema ${name} has no draft in namespace ${namespace}`);
      }
      const model = readSchema(this.loader, String(draft.document), `${namespace}/${name} draft`);
      const hash = hashOf(model.canonical);
      const live = this.row(namespace, name, 'live');
      if (live && live.hash === hash) {
        this.storage.run('DELETE FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?', [namespace, name, DRAFT]);
        return { namespace, name, version: Number(live.version), published: false };
      }
      if (live) {
        this.checkCompatible(namespace, live, model);
      }
      const version = live ? Number(live.version) + 1 : 1;
      this.storage.run(
        `UPDATE engine_schemas SET version = ?, document = ?, hash = ?, published_at = ?
         WHERE namespace = ? AND name = ? AND version = ?`,
        [version, model.canonical, hash, now, namespace, name, DRAFT]
      );
      return { namespace, name, version, published: true };
    });
  }

  /** live returns the live version of a name the namespace reaches. */
  live(name: string, options: SchemaTarget = {}): SchemaRecord | undefined {
    return this.find(name, options, 'live');
  }

  /** draft returns the draft of a name the namespace reaches. */
  draft(name: string, options: SchemaTarget = {}): SchemaRecord | undefined {
    return this.find(name, options, DRAFT);
  }

  /** version returns one published version of a name the namespace reaches. */
  version(name: string, version: number, options: SchemaTarget = {}): SchemaRecord | undefined {
    if (!Number.isInteger(version) || version < 1) {
      throw new EngineError('invalid_argument', `a schema version is a positive integer, got ${String(version)}`);
    }
    return this.find(name, options, version);
  }

  /** list returns every schema name the namespace reaches, its own and the shared namespace's, by name. */
  list(options: SchemaTarget = {}): SchemaSummary[] {
    const namespace = this.namespaces.resolve(options.namespace);
    const byName = new Map<string, SchemaSummary>();
    for (const holder of this.namespaces.lookup(namespace)) {
      const rows = this.storage.all(
        'SELECT name, MAX(version) AS latest, MIN(version) AS earliest FROM engine_schemas WHERE namespace = ? GROUP BY name',
        [holder]
      );
      for (const row of rows) {
        const name = String(row.name);
        if (!byName.has(name)) {
          const latest = Number(row.latest);
          byName.set(name, {
            namespace: holder,
            name,
            liveVersion: latest > DRAFT ? latest : null,
            hasDraft: Number(row.earliest) === DRAFT,
          });
        }
      }
    }
    return [...byName.values()].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  }

  /**
   * validate checks a value as an instance of a name's live version, or of
   * the given version. It throws not_found when there is no such version.
   */
  validate(name: string, value: unknown, options: ValidateOptions = {}): ValidationIssue[] {
    const record =
      options.version === undefined ? this.live(name, options) : this.version(name, options.version, options);
    if (!record) {
      const which = options.version === undefined ? 'no live version' : `no version ${options.version}`;
      throw new EngineError('not_found', `schema ${name} has ${which} in namespace ${this.namespaces.resolve(options.namespace)}`);
    }
    return this.validatorOf(record).validate(value);
  }

  /** validatorOf returns the cached validator of a published version. */
  validatorOf(record: SchemaRecord): SchemaValidator {
    if (record.version === null) {
      throw new EngineError('invalid_argument', `schema ${record.name} is a draft; only a published version validates instances`);
    }
    const key = `${record.namespace}\u0000${record.name}\u0000${record.version}`;
    let validator = this.validators.get(key);
    if (!validator) {
      validator = new SchemaValidator(modelOf(record.canonical));
      this.validators.set(key, validator);
    }
    return validator;
  }

  private find(name: string, options: SchemaTarget, version: number | 'live'): SchemaRecord | undefined {
    const namespace = this.namespaces.resolve(options.namespace);
    checkName(name);
    for (const holder of this.namespaces.lookup(namespace)) {
      const row = this.row(holder, name, version);
      if (row) {
        return toRecord(row);
      }
    }
    return undefined;
  }

  private row(namespace: string, name: string, version: number | 'live'): Row | undefined {
    if (version === 'live') {
      return this.storage.get(
        `SELECT ${COLUMNS} FROM engine_schemas WHERE namespace = ? AND name = ? AND version > ? ORDER BY version DESC LIMIT 1`,
        [namespace, name, DRAFT]
      );
    }
    return this.storage.get(`SELECT ${COLUMNS} FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?`, [
      namespace,
      name,
      version,
    ]);
  }

  // A name lives on one side of a lookup: in the shared namespace, or in
  // the namespaces that look names up there.
  private checkNameSide(namespace: string, name: string): void {
    const shared = this.namespaces.shared;
    if (shared === undefined) {
      return;
    }
    if (namespace !== shared) {
      if (this.storage.get('SELECT 1 AS held FROM engine_schemas WHERE namespace = ? AND name = ? LIMIT 1', [shared, name])) {
        throw new EngineError(
          'name_taken',
          `schema ${name} is defined in the shared namespace ${shared}, which namespace ${namespace} looks names up in; use another name`
        );
      }
      return;
    }
    const other = this.storage.get('SELECT namespace FROM engine_schemas WHERE name = ? AND namespace <> ? LIMIT 1', [name, shared]);
    if (other) {
      throw new EngineError(
        'name_taken',
        `schema ${name} is defined in namespace ${String(other.namespace)}, which looks names up in the shared namespace ${shared}; use another name`
      );
    }
  }

  private checkCompatible(namespace: string, live: Row, model: SchemaModel): void {
    const changes = incompatibleChanges(modelOf(String(live.document)), model);
    if (changes.length > 0) {
      throw new IncompatibleChangeError(namespace, model.name, Number(live.version), changes);
    }
  }
}

function toRecord(row: Row): SchemaRecord {
  const model = modelOf(String(row.document));
  const version = Number(row.version);
  return {
    namespace: String(row.namespace),
    name: String(row.name),
    version: version === DRAFT ? null : version,
    instanceType: model.instanceType,
    document: model.document,
    canonical: model.canonical,
    hash: String(row.hash),
    definedAt: Number(row.defined_at),
    publishedAt: row.published_at === null ? null : Number(row.published_at),
  };
}

function checkName(name: string): void {
  if (typeof name !== 'string' || !SCHEMA_NAME.test(name)) {
    throw new EngineError('invalid_argument', `schema name "${String(name)}" must match ${SCHEMA_NAME.source}`);
  }
}

function hashOf(canonical: string): string {
  return createHash('sha256').update(canonical, 'utf8').digest('hex');
}
