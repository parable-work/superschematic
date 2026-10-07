/*
The schema catalog: the stored schemas, with no access checks (the
registry and the instance store ask the policy first). A schema name has
one draft and a line of published versions in each namespace. define
stores the draft, replacing the one before it, and appends a define event
with the draft's hash; publish makes the draft the next live version (1,
2, 3, ...) and appends a publish event, whose cursor the version keeps.
Instances are read and written with the live version, the newest one.

Both refuse a document the engine does not take (document.ts) and a
version the compatibility rule refuses against the live one (compat.ts),
and a version whose new unique field the stored instances break
(instances/indexes.ts). publish creates and drops the indexes of the
instance type's own fields as the version adds and removes them.
publish loads the draft again, so it also meets the deployment's current
meta-schema. A draft identical to the live version (the same canonical
form, so the same hash) publishes nothing: no version is minted, no event
is appended and the draft is dropped. There is no deprecate or promote: a
live version is replaced only by publishing a newer one, so an older
version never comes back into use.

A schema's instance type may compose behaviors (behaviors/). define and
publish check them against the registered implementations, with the
namespace's other schemas in reach of their configs as the caller may
read them (ConfigTarget.schemas), and the compatibility rule against
each behavior's rule for its config, and each type's display against
the type and its Workflow (display.ts). publish creates the storage of
every behavior the new version composes, in its own transaction, so a
publish that fails leaves none behind, then runs the afterConfigChange
of each behavior whose config the version adds, removes or changes
(behaviors/publish.ts).

The runtime of a version, its validator and its behaviors bound to their
configs and storage, is built once and cached per namespace, name and
version; a version never changes after it is published. A version whose
behaviors this engine cannot run is unavailable, and not cached, so an
implementation registered later makes it available.
*/

import { createHash } from 'node:crypto';

import type { SchemaFileLoader } from '@superschematic/schema-runtime';
import type { Document } from '@superschematic/schema-ir/schema-file';

import type { ConfigSchema, ConfigSchemas } from '../behaviors/behavior.js';
import { checkedTypes, compose, configChanges, configSchemaOf, configTransitions, readTypes, type Composition } from '../behaviors/composition.js';
import type { InstanceValidator, Prefixes } from '../behaviors/execution.js';
import { afterConfigChanges } from '../behaviors/publish.js';
import type { BehaviorRegistry } from '../behaviors/registry.js';
import { prefixOf, storedKey } from '../behaviors/storage.js';
import { EngineError, IncompatibleChangeError, SchemaDocumentError } from '../errors.js';
import { appendEvent, type DefineChange } from '../events/log.js';
import { filterablesOf, type Filterable } from '../instances/filters.js';
import { clashMessage, indexIssues, ownIndexes, scalarFields, syncOwnIndexes, uniqueClashes, type OwnIndex, type UniqueClash } from '../instances/indexes.js';
import type { Namespaces } from '../namespaces.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { incompatibleChanges } from './compat.js';
import { displayIssues } from './display.js';
import { checkSchemaName, modelOf, readSchema, type SchemaModel } from './document.js';
import { SchemaValidator, type NormalizeMode } from './validator.js';

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

/** What running a published version needs: its validator and its behaviors. */
export interface VersionRuntime {
  readonly validator: SchemaValidator;
  readonly composition: Composition;
  /** Each composed behavior's storage prefix, by behavior name. */
  readonly prefixes: Prefixes;
  /** The indexes of the instance type's own fields: its unique fields and its @index entries (instances/indexes.ts). */
  readonly indexes: readonly OwnIndex[];
  /** The own fields an index covers, which an instance's row keeps inline whatever their length. */
  readonly inline: ReadonlySet<string>;
  /** The fields a list filters on, by key (instances/filters.ts). */
  readonly filters: ReadonlyMap<string, Filterable>;
}

/**
 * The access check a define or publish asks, as its caller, before a
 * behavior's config reaches another schema: it throws to refuse.
 */
export type ReadCheck = (schema: string) => void;

/** Who defines or publishes: the event's actor and, for a service's call, its deployable (events/log.ts, actorOf). */
export interface SchemaActor {
  readonly actor: string;
  readonly service?: string;
}

const allowReads: ReadCheck = () => undefined;

const DRAFT = 0;

const COLUMNS = 'namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by';

export class SchemaCatalog {
  private readonly runtimes = new Map<string, VersionRuntime>();
  // What load composed of each model it returned, with no other schema in
  // reach: composeReaching holds each behavior's reads with them to these.
  private readonly alone = new WeakMap<SchemaModel, Composition>();

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly loader: SchemaFileLoader,
    private readonly behaviors: BehaviorRegistry,
    private readonly clock: () => number
  ) {}

  /** read loads a schema document from its JSON text, or from the document as a value. */
  read(input: string | Record<string, unknown>, source: string): SchemaModel {
    return this.load(typeof input === 'string' ? input : JSON.stringify(input), source);
  }

  /**
   * define stores a document as its name's draft in a namespace and
   * appends a define event with its hash. ask is the access check of the
   * caller who defines it, for read on each other schema its behaviors'
   * configs reach (ConfigTarget.schemas); source names the document in
   * errors.
   */
  define(model: SchemaModel, namespace: string, by: SchemaActor, ask: ReadCheck = allowReads, source = 'schema'): SchemaRecord {
    const now = this.clock();
    const hash = hashOf(model.canonical);
    return this.storage.transaction(() => {
      this.checkNameSide(namespace, model.name);
      this.composeReaching(model, namespace, ask, source);
      const live = this.row(namespace, model.name, 'live');
      if (live) {
        this.checkCompatible(namespace, live, model);
        // A unique field the live version lacks holds for the instances
        // stored now; publish asks again, since they may change before it.
        const before = modelOf(String(live.document));
        this.refuseClashes(namespace, live, model, uniqueClashes(this.storage, namespace, model.name, ownIndexes(before, namespace), ownIndexes(model, namespace)));
      }
      this.storage.run(
        `INSERT INTO engine_schemas (${COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)
         ON CONFLICT (namespace, name, version) DO UPDATE
         SET document = excluded.document, hash = excluded.hash,
             defined_at = excluded.defined_at, defined_by = excluded.defined_by`,
        [namespace, model.name, DRAFT, model.canonical, hash, now, by.actor]
      );
      const change: DefineChange = { hash };
      appendEvent(this.storage, {
        kind: 'define',
        namespace,
        schema: model.name,
        instanceId: null,
        seq: null,
        version: null,
        ...by,
        at: now,
        change: JSON.stringify(change),
      });
      return toRecord(this.row(namespace, model.name, DRAFT) as Row);
    });
  }

  /**
   * publish makes the draft of a name the next live version. ask is the
   * access check of the caller who publishes it, as define's is.
   */
  publish(name: string, namespace: string, by: SchemaActor, ask: ReadCheck = allowReads): PublishResult {
    const now = this.clock();
    return this.storage.transaction(() => {
      const draft = this.row(namespace, name, DRAFT);
      if (!draft) {
        throw new EngineError('not_found', `schema ${name} has no draft in namespace ${namespace}`);
      }
      const model = this.load(String(draft.document), `${namespace}/${name} draft`);
      const hash = hashOf(model.canonical);
      const live = this.row(namespace, name, 'live');
      if (live && live.hash === hash) {
        this.storage.run('DELETE FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?', [namespace, name, DRAFT]);
        return { namespace, name, version: Number(live.version), published: false };
      }
      if (live) {
        this.checkCompatible(namespace, live, model);
      }
      const composition = this.composeReaching(model, namespace, ask, `${namespace}/${name} draft`);
      for (const bound of composition.behaviors) {
        this.behaviors.ensureStorage(bound.behavior);
      }
      // The indexes of the instance type's own fields follow the version:
      // the ones it drops go, and a new unique one the stored instances
      // break refuses it.
      const clashes = syncOwnIndexes(
        this.storage,
        namespace,
        name,
        live ? ownIndexes(modelOf(String(live.document)), namespace) : [],
        ownIndexes(model, namespace)
      );
      if (live) {
        this.refuseClashes(namespace, live, model, clashes);
      }
      const version = live ? Number(live.version) + 1 : 1;
      this.storage.run(
        `UPDATE engine_schemas SET version = ?, document = ?, hash = ?, published_at = ?, published_by = ?
         WHERE namespace = ? AND name = ? AND version = ?`,
        [version, model.canonical, hash, now, by.actor, namespace, name, DRAFT]
      );
      afterConfigChanges(this.storage, configTransitions(live ? modelOf(String(live.document)) : undefined, model, this.behaviors), {
        holder: namespace,
        schema: name,
        version,
        now,
        namespaces: namespace === this.namespaces.shared ? this.namespaces.names : [namespace],
        runtime: { composition, validator: lazyValidator(model, composition) },
      });
      const cursor = appendEvent(this.storage, {
        kind: 'publish',
        namespace,
        schema: name,
        instanceId: null,
        seq: null,
        version,
        ...by,
        at: now,
        change: model.canonical,
      });
      // The version keeps its publish's cursor, where a subscription that
      // the version starts begins, after retention prunes the event.
      this.storage.run('UPDATE engine_schemas SET published_cursor = ? WHERE namespace = ? AND name = ? AND version = ?', [cursor, namespace, name, version]);
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
    return this.runtimeOf(record).validator;
  }

  /**
   * runtimeOf returns what running a published version needs, built once.
   * It throws unavailable when the version composes a behavior this engine
   * cannot run.
   */
  runtimeOf(record: SchemaRecord): VersionRuntime {
    if (record.version === null) {
      throw new EngineError('invalid_argument', `schema ${record.name} is a draft; only a published version validates instances`);
    }
    const key = `${record.namespace}\u0000${record.name}\u0000${record.version}`;
    let runtime = this.runtimes.get(key);
    if (!runtime) {
      const model = modelOf(record.canonical);
      const { composition, issues } = compose(model, this.behaviors);
      if (!composition) {
        throw new EngineError(
          'unavailable',
          `schema ${record.name} version ${record.version} in namespace ${record.namespace} composes behaviors this engine cannot run: ${issues.map((issue) => issue.message).join('; ')}`
        );
      }
      const prefixes = new Map<string, string>();
      for (const bound of composition.behaviors) {
        const stored = storedKey(this.storage, bound.behavior.name);
        if (stored === undefined) {
          throw new Error(`behavior ${bound.behavior.name} has no storage in ${this.storage.path}, though version ${record.version} of ${record.name} composes it`);
        }
        prefixes.set(bound.behavior.name, prefixOf(stored));
      }
      const fields = new Map([...composition.fields].map(([field, bound]) => [field, bound.behavior.name]));
      const indexes = ownIndexes(model, record.namespace);
      const filters = filterablesOf(
        scalarFields(model.document, model.instanceType),
        indexes,
        composition.behaviors.map((bound) => ({
          name: bound.behavior.name,
          prefix: prefixes.get(bound.behavior.name) as string,
          filters: bound.behavior.filters,
        }))
      );
      runtime = {
        validator: new SchemaValidator(model, fields),
        composition,
        prefixes,
        indexes,
        inline: new Set(indexes.flatMap((index) => index.keys)),
        filters,
      };
      this.runtimes.set(key, runtime);
    }
    return runtime;
  }

  private load(text: string, source: string): SchemaModel {
    let alone: Composition | undefined;
    const model = readSchema(this.loader, text, source, (candidate) => {
      const composed = compose(candidate, this.behaviors);
      alone = composed.composition;
      // A display is held to the behaviors once they compose (display.ts).
      const issues = composed.composition ? displayIssues(candidate.document, candidate.instanceType, composed.composition) : composed.issues;
      return [...issues, ...indexIssues(candidate.document, candidate.instanceType)];
    });
    if (alone !== undefined) {
      this.alone.set(model, alone);
    }
    return model;
  }

  // composeReaching composes a version being defined or published with
  // the namespace's other schemas in reach of parseConfig, and refuses it
  // as load does for what a config says about them, and for a config that
  // reads other types through ConfigTarget.types with them than load's
  // composition, with none in reach, read.
  private composeReaching(model: SchemaModel, namespace: string, ask: ReadCheck, source: string): Composition {
    const { composition, issues } = compose(model, this.behaviors, this.configSchemas(model, namespace, ask), this.alone.get(model));
    if (!composition) {
      throw new SchemaDocumentError(source, issues);
    }
    return composition;
  }

  // configSchemas is ConfigTarget.schemas for a version being defined or
  // published: its own name is that version; another is its live
  // version, read once, after ask allows it.
  private configSchemas(model: SchemaModel, namespace: string, ask: ReadCheck): ConfigSchemas {
    const read = new Map<string, ConfigSchema | undefined>();
    return Object.freeze({
      get: (name: string): ConfigSchema | undefined => {
        if (name === model.name) {
          return configSchemaOf(model);
        }
        if (!read.has(name)) {
          checkSchemaName(name);
          ask(name);
          const record = this.find(name, namespace, 'live');
          read.set(name, record === undefined ? undefined : configSchemaOf(modelOf(record.canonical)));
        }
        return read.get(name);
      },
    });
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

  // checkCompatible holds a new version to the live one: its fields, those
  // of the types they reach, and those of the types a behavior holds
  // values to, the ones both versions check (checkedTypes) and the ones
  // the live version read through ConfigTarget.types (readTypes), then
  // each behavior's rule for its config.
  private checkCompatible(namespace: string, live: Row, model: SchemaModel): void {
    const before = modelOf(String(live.document));
    const held =
      before.instanceType === model.instanceType
        ? [...new Set([...checkedTypes(before, model, this.behaviors), ...readTypes(before, this.behaviors)])].sort()
        : [];
    const changes = incompatibleChanges(before, model, held);
    if (before.instanceType === model.instanceType) {
      changes.push(...configChanges(before, model, this.behaviors, () => this.hasInstances(namespace, model.name)));
    }
    if (changes.length > 0) {
      throw new IncompatibleChangeError(namespace, model.name, Number(live.version), changes);
    }
  }

  // refuseClashes refuses a version whose new unique fields the stored
  // instances break, naming the values only where the namespace that
  // holds the schema holds the instances.
  private refuseClashes(namespace: string, live: Row, model: SchemaModel, clashes: readonly UniqueClash[]): void {
    if (clashes.length === 0) {
      return;
    }
    throw new IncompatibleChangeError(
      namespace,
      model.name,
      Number(live.version),
      clashes.map((clash) => ({
        path: clash.index.fields.length === 1 ? `${model.instanceType}.${clash.index.fields[0]}` : model.instanceType,
        message: clashMessage(clash, namespace, model.instanceType),
      })),
      'Make the values distinct first (list with where finds the instances that share one), or keep the field as it was'
    );
  }

  // hasInstances reports whether any namespace holds an instance of the
  // schema a namespace holds, its own or, for the shared one, another's.
  private hasInstances(holder: string, name: string): boolean {
    return this.namespaces.names.some((namespace) =>
      this.storage.get('SELECT 1 AS found FROM engine_instances WHERE namespace = ? AND schema = ? AND schema_namespace = ? LIMIT 1', [
        namespace,
        name,
        holder,
      ])
    );
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

// lazyValidator is the validator of a version being published, built at
// its first use: a publish whose afterConfigChange checks no value builds
// none. It is not cached with the runtimes, since the publish may roll
// back.
function lazyValidator(model: SchemaModel, composition: Composition): InstanceValidator {
  let validator: SchemaValidator | undefined;
  const built = (): SchemaValidator =>
    (validator ??= new SchemaValidator(model, new Map([...composition.fields].map(([field, bound]) => [field, bound.behavior.name]))));
  return {
    validate: (value: unknown) => built().validate(value),
    normalize: (value: unknown, mode: NormalizeMode) => built().normalize(value, mode),
    validateType: (type: string, value: unknown, path: string) => built().validateType(type, value, path),
  };
}

function hashOf(canonical: string): string {
  return createHash('sha256').update(canonical, 'utf8').digest('hex');
}
