/*
The schema catalog: the stored schemas, with no access checks (the
registry and the instance store ask the policy first). A schema name has
one draft and a line of published versions in each namespace. define
stores the draft, replacing the one before it; publish makes the draft the
next live version (1, 2, 3, ...) and appends a publish event. Instances
are read and written with the live version, the newest one.

Both refuse a document the engine does not take (document.ts) and a
version the compatibility rule refuses against the live one (compat.ts).
publish loads the draft again, so it also meets the deployment's current
meta-schema. A draft identical to the live version (the same canonical
form, so the same hash) publishes nothing: no version is minted, no event
is appended and the draft is dropped. There is no deprecate or promote: a
live version is replaced only by publishing a newer one, so an older
version never comes back into use.

The validator of a version is built once and cached per namespace, name
and version; a version never changes after it is published.
*/

import { createHash } from 'node:crypto';

import type { SchemaFileLoader } from '@superschematic/schema-runtime';
import type { Document } from '@superschematic/schema-ir/schema-file';

import { EngineError, IncompatibleChangeError } from '../errors.js';
import { appendEvent } from '../events/log.js';
import type { Namespaces } from '../namespaces.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { incompatibleChanges } from './compat.js';
import { modelOf, readSchema, type SchemaModel } from './document.js';
import { SchemaValidator } from './validator.js';

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
  /** Who defined it; null for a version stored before actors were recorded. */
  definedBy: string | null;
  /** When it was published; null for the draft. */
  publishedAt: number | null;
  /** Who published it; null for the draft. */
  publishedBy: string | null;
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

const DRAFT = 0;

const COLUMNS = 'namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by';

export class SchemaCatalog {
  private readonly validators = new Map<string, SchemaValidator>();

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly loader: SchemaFileLoader,
    private readonly clock: () => number
  ) {}

  /** read loads a schema document from its JSON text, or from the document as a value. */
  read(input: string | Record<string, unknown>, source: string): SchemaModel {
    return readSchema(this.loader, typeof input === 'string' ? input : JSON.stringify(input), source);
  }

  /** define stores a document as its name's draft in a namespace. */
  define(model: SchemaModel, namespace: string, actor: string): SchemaRecord {
    const now = this.clock();
    return this.storage.transaction(() => {
      this.checkNameSide(namespace, model.name);
      const live = this.row(namespace, model.name, 'live');
      if (live) {
        this.checkCompatible(namespace, live, model);
      }
      this.storage.run(
        `INSERT INTO engine_schemas (${COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)
         ON CONFLICT (namespace, name, version) DO UPDATE
         SET document = excluded.document, hash = excluded.hash,
             defined_at = excluded.defined_at, defined_by = excluded.defined_by`,
        [namespace, model.name, DRAFT, model.canonical, hashOf(model.canonical), now, actor]
      );
      return toRecord(this.row(namespace, model.name, DRAFT) as Row);
    });
  }

  /** publish makes the draft of a name the next live version. */
  publish(name: string, namespace: string, actor: string): PublishResult {
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
        `UPDATE engine_schemas SET version = ?, document = ?, hash = ?, published_at = ?, published_by = ?
         WHERE namespace = ? AND name = ? AND version = ?`,
        [version, model.canonical, hash, now, actor, namespace, name, DRAFT]
      );
      appendEvent(this.storage, {
        kind: 'publish',
        namespace,
        schema: name,
        instanceId: null,
        seq: null,
        version,
        actor,
        at: now,
        change: model.canonical,
      });
      return { namespace, name, version, published: true };
    });
  }

  /**
   * find returns the live version, the draft or one version of a name,
   * looked up in the namespace, then in the shared one.
   */
  find(name: string, namespace: string, version: number | 'live' | 'draft'): SchemaRecord | undefined {
    for (const holder of this.namespaces.lookup(namespace)) {
      const row = this.row(holder, name, version === 'draft' ? DRAFT : version);
      if (row) {
        return toRecord(row);
      }
    }
    return undefined;
  }

  /** list returns every schema name the namespace reaches, its own and the shared namespace's, by name. */
  list(namespace: string): SchemaSummary[] {
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
    // The message does not name the namespace that holds the name, so a
    // caller in the shared namespace learns nothing about another one.
    if (this.storage.get('SELECT 1 AS held FROM engine_schemas WHERE name = ? AND namespace <> ? LIMIT 1', [name, shared])) {
      throw new EngineError(
        'name_taken',
        `schema ${name} is defined in a namespace that looks names up in the shared namespace ${shared}; use another name`
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
    definedBy: row.defined_by === null ? null : String(row.defined_by),
    publishedAt: row.published_at === null ? null : Number(row.published_at),
    publishedBy: row.published_by === null ? null : String(row.published_by),
  };
}

function hashOf(canonical: string): string {
  return createHash('sha256').update(canonical, 'utf8').digest('hex');
}
