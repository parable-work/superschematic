/*
Schemas (D16): the drafts and published versions engine.schemas defines
and publishes per namespace, what a define and a validate take, and what
the access policy lets a principal do with each.
*/

import type { BehaviorDeclaration } from './declarations.js';

/**
 * A schema-file document as JSON: the engine reads it against the
 * meta-schema, so its type here is an object; @superschematic/schema-ir's
 * schema-file Document types it member by member.
 */
export type SchemaFileDocument = Record<string, unknown>;

/** A stored draft or published version of a schema. */
export interface SchemaRecord {
  /** The namespace that holds the schema. */
  namespace: string;
  name: string;
  /** The published version, or null for the draft. */
  version: number | null;
  /** The type that holds instances. */
  instanceType: string;
  /** The schema-file document (ir/typescript/schema-file.json describes it), as JSON. */
  document: SchemaFileDocument;
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

/** A schema name: a letter, then letters, digits, `_` and `-`, at most 128 characters. */
export const SCHEMA_NAME = /^[A-Za-z][A-Za-z0-9_-]{0,127}$/;
