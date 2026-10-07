/*
A refusal crosses HTTP as an RFC 9457 problem document (runtime/engine/
README.md, "Statuses"), and the client turns it back into an error a
caller branches on without parsing prose: EngineProblem carries the
status, the problem's code, the request id and the details, with
details.issues read into `issues`; EngineVeto, for code `vetoed`, also
the refusing behavior, what it refused, its reason and the veto's own
code and details. A request that never got an answer is an
EngineTransportError.
*/

/** One reason the engine refused an argument, a document or an instance. */
export interface ProblemIssue {
  /**
   * Where: a JSON pointer for a schema document, an operation's or a
   * create's parameters and preconditions (`/Lease/token`); a field path
   * for an instance (`lines[2].sku`, `''` for the instance itself).
   */
  readonly path: string;
  /** The rule an instance's field breaks (`required`, `readOnly`, `constant`, ...); absent for the other issues. */
  readonly rule?: string;
  readonly message: string;
}

/** One change a refused schema version makes (`incompatible_change`). */
export interface ProblemChange {
  readonly path: string;
  readonly message: string;
}

/** An RFC 9457 problem document as the engine writes it. */
export interface ProblemDocument {
  type: string;
  title: string;
  status: number;
  detail: string;
  code?: string;
  requestId?: string;
  details?: unknown;
  [extension: string]: unknown;
}

/** A refusal the engine answered with a problem document, or a failure status with none. */
export class EngineProblem extends Error {
  /** The HTTP status. */
  readonly status: number;
  /** The problem's `code`: `not_found`, `vetoed`, `seq_mismatch`, `service_unauthorized`, ...; absent when the answer carried none. */
  readonly code: string | undefined;
  readonly title: string;
  readonly detail: string;
  readonly requestId: string | undefined;
  /** The problem's `details`, as sent. */
  readonly details: unknown;
  /** `details.issues`: why an argument, a document or an instance was refused, each at its path; empty when there are none. */
  readonly issues: readonly ProblemIssue[];
  /** `details.changes` of an `incompatible_change`; empty otherwise. */
  readonly changes: readonly ProblemChange[];
  /** `details.fields` of a `conflict` over unique fields, the fields another instance holds the values of; empty otherwise. */
  readonly fields: readonly string[];
  /**
   * `details.floor` of a `cursor_expired` (410): the earliest cursor a read
   * of the namespace may start from, retention having pruned the events
   * before it; undefined otherwise.
   */
  readonly floor: number | undefined;
  /** `details.head` of a `cursor_expired`: the log's last cursor, to start again from; undefined otherwise. */
  readonly head: number | undefined;
  /** Seconds to wait before trying again, from `Retry-After` (429). */
  readonly retryAfter: number | undefined;
  /** The document as the engine sent it; a document of `about:blank` built from the status when the answer was not one. */
  readonly problem: ProblemDocument;

  constructor(problem: ProblemDocument, retryAfter?: number) {
    super(problem.detail || problem.title || `HTTP ${problem.status}`);
    this.name = 'EngineProblem';
    this.status = problem.status;
    this.code = typeof problem.code === 'string' ? problem.code : undefined;
    this.title = problem.title;
    this.detail = problem.detail;
    this.requestId = typeof problem.requestId === 'string' ? problem.requestId : undefined;
    this.details = problem.details;
    this.issues = issuesOf(problem.details);
    this.changes = changesOf(problem.details);
    this.fields = fieldsOf(problem.details);
    this.floor = numberOf(problem.details, 'floor');
    this.head = numberOf(problem.details, 'head');
    this.retryAfter = retryAfter;
    this.problem = problem;
  }
}

/**
 * A behavior refused a create, an update, a delete or an operation (code
 * `vetoed`, 409). A client branches on `behavior` and `vetoCode`, which
 * the behavior's declaration lists: `Lease` and `token_stale` say another
 * lease replaced the caller's.
 */
export class EngineVeto extends EngineProblem {
  /** The behavior that refused. */
  readonly behavior: string;
  /** What it refused: `create`, `update`, `delete`, or the operation's name. */
  readonly action: string;
  readonly reason: string;
  /** The veto's code; undefined for a veto that gives none. */
  readonly vetoCode: string | undefined;
  /** The veto's details, a JSON object; undefined when it gives none. */
  readonly vetoDetails: Readonly<Record<string, unknown>> | undefined;

  constructor(problem: ProblemDocument, retryAfter?: number) {
    super(problem, retryAfter);
    this.name = 'EngineVeto';
    const details = (isObject(problem.details) ? problem.details : {}) as Record<string, unknown>;
    this.behavior = typeof details.behavior === 'string' ? details.behavior : '';
    this.action = typeof details.action === 'string' ? details.action : '';
    this.reason = typeof details.reason === 'string' ? details.reason : '';
    this.vetoCode = typeof details.code === 'string' ? details.code : undefined;
    this.vetoDetails = isObject(details.details) ? (details.details as Record<string, unknown>) : undefined;
  }
}

/** The request got no answer: the network failed, or the client's timeout passed. A caller's own abort rethrows its reason instead. */
export class EngineTransportError extends Error {
  /** Whether the client's timeout ended it. */
  readonly timedOut: boolean;

  constructor(message: string, options: { cause?: unknown; timedOut?: boolean } = {}) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = 'EngineTransportError';
    this.timedOut = options.timedOut ?? false;
  }
}

/**
 * isVeto says whether an error is a veto, optionally of one behavior and
 * with one of the codes given: `isVeto(error, 'Lease', ['lapsed', 'token_stale'])`.
 */
export function isVeto(error: unknown, behavior?: string, codes?: string | readonly string[]): error is EngineVeto {
  if (!(error instanceof EngineVeto)) {
    return false;
  }
  if (behavior !== undefined && error.behavior !== behavior) {
    return false;
  }
  if (codes === undefined) {
    return true;
  }
  const wanted = typeof codes === 'string' ? [codes] : codes;
  return error.vetoCode !== undefined && wanted.includes(error.vetoCode);
}

/** isProblem says whether an error is an engine problem, optionally with one of the codes given. */
export function isProblem(error: unknown, codes?: string | readonly string[]): error is EngineProblem {
  if (!(error instanceof EngineProblem)) {
    return false;
  }
  if (codes === undefined) {
    return true;
  }
  const wanted = typeof codes === 'string' ? [codes] : codes;
  return error.code !== undefined && wanted.includes(error.code);
}

/**
 * problemFrom reads a failure answer: the problem document when the body
 * is one, else one built from the status, so a proxy's HTML page still
 * answers as a problem with that status and no code.
 */
export function problemFrom(status: number, statusText: string, body: unknown, retryAfter?: number): EngineProblem {
  const problem: ProblemDocument =
    isObject(body) && typeof (body as Record<string, unknown>).status === 'number'
      ? ({ type: 'about:blank', title: statusText, detail: '', ...(body as Record<string, unknown>) } as ProblemDocument)
      : { type: 'about:blank', title: statusText || `HTTP ${status}`, status, detail: typeof body === 'string' ? body.slice(0, 500) : '' };
  return problem.code === 'vetoed' ? new EngineVeto(problem, retryAfter) : new EngineProblem(problem, retryAfter);
}

function issuesOf(details: unknown): ProblemIssue[] {
  if (!isObject(details) || !Array.isArray((details as Record<string, unknown>).issues)) {
    return [];
  }
  return ((details as Record<string, unknown>).issues as unknown[]).filter(isObject).map((issue) => {
    const { path, rule, message } = issue as Record<string, unknown>;
    return {
      path: typeof path === 'string' ? path : '',
      ...(typeof rule === 'string' ? { rule } : {}),
      message: typeof message === 'string' ? message : '',
    };
  });
}

function changesOf(details: unknown): ProblemChange[] {
  if (!isObject(details) || !Array.isArray((details as Record<string, unknown>).changes)) {
    return [];
  }
  return ((details as Record<string, unknown>).changes as unknown[]).filter(isObject).map((change) => {
    const { path, message } = change as Record<string, unknown>;
    return { path: typeof path === 'string' ? path : '', message: typeof message === 'string' ? message : '' };
  });
}

function fieldsOf(details: unknown): string[] {
  if (!isObject(details) || !Array.isArray((details as Record<string, unknown>).fields)) {
    return [];
  }
  return ((details as Record<string, unknown>).fields as unknown[]).filter((field): field is string => typeof field === 'string');
}

function numberOf(details: unknown, key: string): number | undefined {
  const value = isObject(details) ? (details as Record<string, unknown>)[key] : undefined;
  return typeof value === 'number' ? value : undefined;
}

function isObject(value: unknown): value is object {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
