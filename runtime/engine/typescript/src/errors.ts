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
  | 'invalid_argument'
  /** The access policy refuses the call, or a behavior a caller without the permission its config names. */
  | 'forbidden'
  /** The instance, or an update's result, does not validate against the live version. */
  | 'invalid_instance'
  /** An instance with the id already exists. */
  | 'conflict'
  /** An update or delete named the instance's sequence, and the instance is no longer at it. */
  | 'seq_mismatch'
  /** A behavior's guard refuses the change. */
  | 'vetoed'
  /**
   * The schema's live version composes a behavior this engine cannot run:
   * no implementation is registered for it, or the registered one refuses
   * the version's config.
   */
  | 'unavailable';

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
      `schema ${name} in namespace ${namespace}: a new version cannot replace version ${liveVersion}: ${changes.map((change) => change.message).join('; ')}. A new version may only add optional fields and enum values, widen bounds, change documentation and make the behavior changes each behavior allows; publish any other change under a new schema name`
    );
    this.name = 'IncompatibleChangeError';
    this.changes = changes;
  }
}

/** One reason an instance is refused, at a path such as `lines[2].sku`; an empty path is the instance itself. */
export interface ValidationIssue {
  path: string;
  /**
   * The rule it breaks: the schema runtime's (`required`, `type`, `pattern`,
   * `enum`, ...), `unknown` for an undeclared key, or `readOnly` for a field
   * a behavior adds, which only its operations change.
   */
  rule: string;
  message: string;
}

/** An instance the live version of its schema refuses. */
export class InstanceValidationError extends EngineError {
  readonly issues: ValidationIssue[];

  constructor(namespace: string, schema: string, version: number, issues: ValidationIssue[]) {
    super(
      'invalid_instance',
      `${schema} in namespace ${namespace} (version ${version}): ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`
    );
    this.name = 'InstanceValidationError';
    this.issues = issues;
  }
}

/** A guard of a behavior on the type refused an update, a delete or an operation. */
export class BehaviorVetoError extends EngineError {
  /** The behavior whose guard refused. */
  readonly behavior: string;
  /** What it refused: `update`, `delete`, or the operation's name. */
  readonly action: string;
  readonly reason: string;

  constructor(behavior: string, action: string, schema: string, id: string, reason: string) {
    super('vetoed', `behavior ${behavior} vetoes ${action} of ${schema} ${id}: ${reason}`);
    this.name = 'BehaviorVetoError';
    this.behavior = behavior;
    this.action = action;
    this.reason = reason;
  }
}

/** An operation's parameters, refused by its paramsSchema; each issue is at a JSON pointer into them. */
export class OperationParamsError extends EngineError {
  readonly issues: SchemaIssue[];

  constructor(behavior: string, operation: string, issues: SchemaIssue[]) {
    super(
      'invalid_argument',
      `operation ${operation} of behavior ${behavior}: ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`
    );
    this.name = 'OperationParamsError';
    this.issues = issues;
  }
}

/**
 * A defect in a behavior's code rather than a refused request: a result its
 * resultSchema refuses, a field value that is not JSON, SQL outside its own
 * storage, a write from a read-only context, a promise from a synchronous
 * call. It is not an EngineError, so a server answers it as an internal
 * error; the write it happened in rolls back.
 */
export class BehaviorError extends Error {
  readonly behavior: string;

  constructor(behavior: string, message: string) {
    super(`behavior ${behavior}: ${message}`);
    this.name = 'BehaviorError';
    this.behavior = behavior;
  }
}
