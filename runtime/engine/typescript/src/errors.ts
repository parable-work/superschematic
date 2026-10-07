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
  /** The namespace is archived: it is read as it was, and refuses every write. */
  | 'namespace_archived'
  /**
   * A read of the event log from a cursor retention has pruned past: the
   * events after it are no longer all there (CursorExpiredError).
   */
  | 'cursor_expired'
  | 'invalid_argument'
  /** The access policy refuses the call, or a behavior a caller without the permission its config names. */
  | 'forbidden'
  /** The instance, or an update's result, does not validate against the live version. */
  | 'invalid_instance'
  /** An instance with the id already exists, or another instance holds the values of a unique field (UniqueConflictError). */
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

/**
 * A read of the event log, or a stream's resume, from a cursor before a
 * namespace's floor: retention pruned events after the cursor, so the
 * read would not be complete. floor is the earliest cursor a read of the
 * namespace may start from, head the log's last; a client that can take
 * the gap reads on from floor, and one that cannot starts again from
 * head.
 */
export class CursorExpiredError extends EngineError {
  readonly after: number;
  readonly floor: number;
  readonly head: number;

  constructor(namespace: string, after: number, floor: number, head: number) {
    super(
      'cursor_expired',
      `the event log of namespace ${namespace} no longer holds every event after cursor ${after}: retention pruned it through ${floor}; read from ${floor} to take what is left, or from head`
    );
    this.name = 'CursorExpiredError';
    this.after = after;
    this.floor = floor;
    this.head = head;
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

/** What IncompatibleChangeError tells a definer to do, unless the caller says otherwise. */
const INCOMPATIBLE_ADVICE =
  'A new version may only add optional fields and enum values, widen bounds, change documentation and make the behavior changes each behavior allows; publish any other change under a new schema name';

/** A new version the compatibility rule refuses. */
export class IncompatibleChangeError extends EngineError {
  readonly changes: SchemaChange[];

  /** advice replaces the closing sentence, for a change the stored instances refuse rather than the rule's diff (a new unique field). */
  constructor(namespace: string, name: string, liveVersion: number, changes: SchemaChange[], advice = INCOMPATIBLE_ADVICE) {
    super(
      'incompatible_change',
      `schema ${name} in namespace ${namespace}: a new version cannot replace version ${liveVersion}: ${changes.map((change) => change.message).join('; ')}. ${advice}`
    );
    this.name = 'IncompatibleChangeError';
    this.changes = changes;
  }
}

/**
 * A create or an update that would give a second instance of a namespace
 * the values of a unique field, or of a unique index's fields: fields
 * names them by JSON key. The instance that holds them is not named, since
 * the caller may write the schema without reading it.
 */
export class UniqueConflictError extends EngineError {
  /** The JSON keys of the unique fields, in the index's order. */
  readonly fields: string[];

  constructor(namespace: string, schema: string, fields: readonly string[], values: readonly unknown[]) {
    const held = fields.map((field, position) => `${field} ${shownValue(values[position])}`);
    super(
      'conflict',
      `${schema} in namespace ${namespace}: another instance holds ${held.length < 2 ? held.join('') : `${held.slice(0, -1).join(', ')} and ${held[held.length - 1]}`}, and ${fields.length > 1 ? 'together they are' : 'it is'} unique`
    );
    this.name = 'UniqueConflictError';
    this.fields = [...fields];
  }
}

// shownValue writes a value for a message, cut short past 80 characters.
function shownValue(value: unknown): string {
  const text = JSON.stringify(value) ?? String(value);
  return text.length > 80 ? `${text.slice(0, 77)}...` : text;
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

/**
 * A behavior's refusal, as a guard returns it and a handler throws it: a
 * reason, and optionally a code the behavior's declaration lists (its
 * `vetoes`), so a client branches on the code rather than the prose, with
 * details that are a JSON object.
 */
export interface Veto {
  readonly reason: string;
  /** Lowercase snake case, one its behavior's declaration lists; read beside the behavior's name. */
  readonly code?: string;
  readonly details?: Readonly<Record<string, unknown>>;
}

/**
 * A behavior refused a create, an update, a delete or an operation: its
 * guard, its initialize, or the handler of one of its operations.
 * vetoCode and vetoDetails are the veto's code and details; the problem
 * document carries them as details.code and details.details.
 */
export class BehaviorVetoError extends EngineError {
  /** The behavior that refused. */
  readonly behavior: string;
  /** What it refused: `create`, `update`, `delete`, or the operation's name. */
  readonly action: string;
  readonly reason: string;
  /** The veto's code, one its behavior's declaration lists; undefined for a veto that gives none. */
  readonly vetoCode: string | undefined;
  /** The veto's details, a JSON object; undefined when it gives none. */
  readonly vetoDetails: Readonly<Record<string, unknown>> | undefined;

  constructor(behavior: string, action: string, schema: string, id: string, veto: string | Veto) {
    const { reason, code, details } = typeof veto === 'string' ? { reason: veto, code: undefined, details: undefined } : veto;
    super('vetoed', `behavior ${behavior} vetoes ${action} of ${schema} ${id}: ${reason}`);
    this.name = 'BehaviorVetoError';
    this.behavior = behavior;
    this.action = action;
    this.reason = reason;
    this.vetoCode = code;
    this.vetoDetails = details;
  }
}

/**
 * The preconditions of an update, a delete or an operation, refused: an
 * entry for a behavior the type does not compose or that declares no
 * preconditionSchema, or one its schema refuses. Each issue is at a JSON
 * pointer into the preconditions, `/<Behavior>/<member>`.
 */
export class PreconditionsError extends EngineError {
  readonly issues: SchemaIssue[];

  constructor(schema: string, issues: SchemaIssue[]) {
    super(
      'invalid_argument',
      `preconditions of ${schema}: ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`
    );
    this.name = 'PreconditionsError';
    this.issues = issues;
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
 * A create's parameters for the type's behaviors, refused: an entry for a
 * behavior the type does not compose or that takes none, one its
 * createParamsSchema refuses, or one its behavior refuses for its config
 * (a link the config does not give, a required link not given). Each
 * issue is at a JSON pointer into the create's arguments, under
 * /behaviors/<behavior>.
 */
export class CreateParamsError extends EngineError {
  readonly issues: SchemaIssue[];

  constructor(schema: string, issues: SchemaIssue[]) {
    super(
      'invalid_argument',
      `create of ${schema}: ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`
    );
    this.name = 'CreateParamsError';
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
