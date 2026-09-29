/*
Errors the engine throws on a request it refuses. Each carries a code a
server maps to a status; the HTTP and MCP surfaces read the code, not the
message.
*/

export type EngineErrorCode =
  /** The schema document is not one the engine takes. */
  | 'invalid_schema'
  /** A new version changes the schema in a way stored instances may not satisfy. */
  | 'incompatible_change'
  /** The schema name is defined on the other side of a namespace lookup (shared or not). */
  | 'name_taken'
  | 'not_found'
  | 'unknown_namespace'
  | 'invalid_argument';

export class EngineError extends Error {
  readonly code: EngineErrorCode;

  constructor(code: EngineErrorCode, message: string) {
    super(message);
    this.name = 'EngineError';
    this.code = code;
  }
}

/** One reason a schema document is refused, at a JSON pointer into it. */
export interface SchemaIssue {
  path: string;
  message: string;
}

/** A schema document the engine refuses: the loader's issues or its own. */
export class SchemaDocumentError extends EngineError {
  readonly issues: SchemaIssue[];

  constructor(source: string, issues: SchemaIssue[]) {
    super('invalid_schema', `${source}: ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`);
    this.name = 'SchemaDocumentError';
    this.issues = issues;
  }
}

/** One change a new version makes that a stored instance may not satisfy. */
export interface SchemaChange {
  /** What changed: a type, `Type.field`, or an enum. */
  path: string;
  message: string;
}

/** A new version the compatibility rule refuses. */
export class IncompatibleChangeError extends EngineError {
  readonly changes: SchemaChange[];

  constructor(namespace: string, name: string, liveVersion: number, changes: SchemaChange[]) {
    super(
      'incompatible_change',
      `schema ${name} in namespace ${namespace}: a new version cannot replace version ${liveVersion}: ${changes.map((change) => change.message).join('; ')}. A new version may only add optional fields and enum values, widen bounds and change documentation; publish any other change under a new schema name`
    );
    this.name = 'IncompatibleChangeError';
    this.changes = changes;
  }
}
