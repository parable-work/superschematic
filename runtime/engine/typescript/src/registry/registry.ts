/*
The schema registry, as callers use it: each method takes the principal
it acts for and asks the access policy (`define`, `publish`, `read`)
before it touches the catalog (catalog.ts), which holds the rules.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import { EngineError, type ValidationIssue } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import type { PublishResult, SchemaCatalog, SchemaRecord, SchemaSummary } from './catalog.js';
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

export class SchemaRegistry {
  constructor(
    private readonly catalog: SchemaCatalog,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {}

  /**
   * define stores a schema document as the draft of its name in a
   * namespace, replacing the draft before it. input is the document's JSON
   * text, or the document as a value.
   */
  define(principal: Principal, input: string | Record<string, unknown>, options: DefineOptions = {}): SchemaRecord {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const model = this.catalog.read(input, options.source ?? 'schema');
    this.access.require(principal, 'define', namespace, model.name);
    return this.catalog.define(model, namespace, principal.subject);
  }

  /** publish makes the draft of a name the next live version. */
  publish(principal: Principal, name: string, options: SchemaTarget = {}): PublishResult {
    const namespace = this.target(principal, name, options);
    this.access.require(principal, 'publish', namespace, name);
    return this.catalog.publish(name, namespace, principal.subject);
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
   * validate checks a value as an instance of a name's live version, or of
   * the given version. It throws not_found when there is no such version.
   */
  validate(principal: Principal, name: string, value: unknown, options: ValidateOptions = {}): ValidationIssue[] {
    return this.validator(principal, name, options).validate(value);
  }

  /**
   * validator returns the validator of a name's live version, or of the
   * given version, built once per namespace, name and version.
   */
  validator(principal: Principal, name: string, options: ValidateOptions = {}): SchemaValidator {
    if (options.version !== undefined) {
      checkVersion(options.version);
    }
    const namespace = this.readable(principal, name, options);
    const record = this.catalog.find(name, namespace, options.version ?? 'live');
    if (!record) {
      const which = options.version === undefined ? 'no live version' : `no version ${options.version}`;
      throw new EngineError('not_found', `schema ${name} has ${which} in namespace ${namespace}`);
    }
    return this.catalog.validatorOf(record);
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
