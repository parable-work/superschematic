/*
The schema registry, as callers use it: each method takes the principal
it acts for and asks the access policy (`define`, `publish`, `read`)
before it touches the catalog (catalog.ts), which holds the rules. An
archived namespace refuses a define and a publish once the policy has
allowed them (namespace_archived), and is read as it was.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import type { BehaviorDeclaration } from '../behaviors/declaration.js';
import { EngineError, type ValidationIssue } from '../errors.js';
import { actorOf } from '../events/log.js';
import type { Namespaces } from '../namespaces.js';
import type { PublishResult, ReadCheck, SchemaCatalog, SchemaRecord, SchemaSummary } from './catalog.js';
import { checkSchemaName } from './document.js';
import type { SchemaValidator } from './validator.js';

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

/**
 * What the access policy lets a principal do with one live schema: read is
 * always true, since capabilities lists only the schemas the principal may
 * read.
 */
export interface SchemaCapabilities {
  name: string;
  read: boolean;
  write: boolean;
  define: boolean;
  publish: boolean;
}

/** A behavior a schema's instance type composes. */
export interface ComposedBehavior {
  name: string;
  /** The type's config of it, as the schema holds it; {} when it gives none. */
  config: unknown;
  /** The behavior's declaration: its fields and operations. */
  declaration: BehaviorDeclaration;
}

export class SchemaRegistry {
  constructor(
    private readonly catalog: SchemaCatalog,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {}

  /**
   * define stores a schema document as the draft of its name in a
   * namespace, replacing the draft before it, and appends a define event
   * with the draft's hash. input is the document's JSON text, or the
   * document as a value.
   */
  define(principal: Principal, input: string | Record<string, unknown>, options: DefineOptions = {}): SchemaRecord {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const model = this.catalog.read(input, options.source ?? 'schema');
    this.access.require(principal, 'define', namespace, model.name);
    this.namespaces.requireWritable(namespace);
    return this.catalog.define(model, namespace, actorOf(principal), this.reads(principal, namespace), options.source ?? 'schema');
  }

  /** publish makes the draft of a name the next live version. */
  publish(principal: Principal, name: string, options: SchemaTarget = {}): PublishResult {
    const namespace = this.target(principal, name, options);
    this.access.require(principal, 'publish', namespace, name);
    this.namespaces.requireWritable(namespace);
    return this.catalog.publish(name, namespace, actorOf(principal), this.reads(principal, namespace));
  }

  // reads is the check a define or publish asks before a behavior's
  // config reaches another schema: read, as the caller.
  private reads(principal: Principal, namespace: string): ReadCheck {
    return (schema) => this.access.require(principal, 'read', namespace, schema);
  }

  /** live returns the live version of a name the namespace reaches. */
  live(principal: Principal, name: string, options: SchemaTarget = {}): SchemaRecord | undefined {
    return this.catalog.find(name, this.readable(principal, name, options), 'live');
  }

  /** draft returns the draft of a name the namespace reaches. */
  draft(principal: Principal, name: string, options: SchemaTarget = {}): SchemaRecord | undefined {
    return this.catalog.find(name, this.readable(principal, name, options), 'draft');
  }

  /** version returns one published version of a name the namespace reaches. */
  version(principal: Principal, name: string, version: number, options: SchemaTarget = {}): SchemaRecord | undefined {
    checkVersion(version);
    return this.catalog.find(name, this.readable(principal, name, options), version);
  }

  /**
   * list returns the schema names the namespace reaches, its own and the
   * shared namespace's, that the principal may read, by name.
   */
  list(principal: Principal, options: SchemaTarget = {}): SchemaSummary[] {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    return this.catalog.list(namespace).filter((summary) => this.access.allows(principal, 'read', namespace, summary.name));
  }

  /**
   * capabilities answers, for each schema with a live version that the
   * namespace reaches and the principal may read, by name, whether the
   * access policy allows each action, as it would answer the calls. A
   * schema the principal may not read is left out, as list leaves it out,
   * and a draft that was never published has no live version. A behavior
   * operation asks its action, so it is not listed apart; a call the
   * policy allows may still be refused for another reason, a name the
   * shared namespace holds say. An archived namespace refuses every
   * write, so it answers write, define and publish false whatever the
   * policy says.
   */
  capabilities(principal: Principal, options: SchemaTarget = {}): SchemaCapabilities[] {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const archived = this.namespaces.archived(namespace);
    const out: SchemaCapabilities[] = [];
    for (const { name, liveVersion } of this.catalog.list(namespace)) {
      if (liveVersion === null || !this.access.allows(principal, 'read', namespace, name)) {
        continue;
      }
      const allows = (action: 'write' | 'define' | 'publish') => !archived && this.access.allows(principal, action, namespace, name);
      out.push({ name, read: true, write: allows('write'), define: allows('define'), publish: allows('publish') });
    }
    return out;
  }

  /**
   * validate checks a value as an instance of a name's live version, or of
   * the given version, as a create would: the value as the version stores
   * it, its scalars' values normalized and its defaults filled, then, when
   * the version refuses nothing, what a scalar's parser refused. It throws
   * not_found when there is no such version.
   */
  validate(principal: Principal, name: string, value: unknown, options: ValidateOptions = {}): ValidationIssue[] {
    const validator = this.validator(principal, name, options);
    const normalized = validator.normalize(value, 'create');
    const issues = validator.validate(normalized.value);
    return issues.length > 0 ? issues : normalized.issues;
  }

  /**
   * validator returns the validator of a name's live version, or of the
   * given version, built once per namespace, name and version.
   */
  validator(principal: Principal, name: string, options: ValidateOptions = {}): SchemaValidator {
    return this.catalog.validatorOf(this.published(principal, name, options));
  }

  /**
   * behaviors returns the behaviors the instance type of a name's live
   * version, or of the given version, composes, in list order. It throws
   * unavailable when this engine cannot run one of them.
   */
  behaviors(principal: Principal, name: string, options: ValidateOptions = {}): ComposedBehavior[] {
    return this.catalog.runtimeOf(this.published(principal, name, options)).composition.behaviors.map((bound) => ({
      name: bound.behavior.name,
      config: bound.json,
      declaration: bound.behavior.declaration,
    }));
  }

  private published(principal: Principal, name: string, options: ValidateOptions): SchemaRecord {
    if (options.version !== undefined) {
      checkVersion(options.version);
    }
    const namespace = this.readable(principal, name, options);
    const record = this.catalog.find(name, namespace, options.version ?? 'live');
    if (!record) {
      const which = options.version === undefined ? 'no live version' : `no version ${options.version}`;
      throw new EngineError('not_found', `schema ${name} has ${which} in namespace ${namespace}`);
    }
    return record;
  }

  private target(principal: Principal, name: string, options: SchemaTarget): string {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    checkSchemaName(name);
    return namespace;
  }

  private readable(principal: Principal, name: string, options: SchemaTarget): string {
    const namespace = this.target(principal, name, options);
    this.access.require(principal, 'read', namespace, name);
    return namespace;
  }
}

function checkVersion(version: number): void {
  if (!Number.isInteger(version) || version < 1) {
    throw new EngineError('invalid_argument', `a schema version is a positive integer, got ${String(version)}`);
  }
}
