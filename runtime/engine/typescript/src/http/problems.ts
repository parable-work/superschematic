/*
How an engine refusal crosses HTTP. Each EngineError code has one status,
and the problem document carries the code as its `code` member; a refused
schema document, instance, operation's parameters, create's parameters
or preconditions carries its issues as `details.issues`, a refused
version its changes as `details.changes`, a write that repeats a unique
field's value the fields as `details.fields`, a read from a cursor
retention has pruned past the cursor, the floor and the head as
`details.after`, `details.floor` and `details.head`, and a behavior's
veto the behavior, what it refused and why as `details`, with the veto's
own code and details, when it gives them, as `details.code` and
`details.details`:
a client branches on `code: "vetoed"` and then on the behavior and its
code. The detail is the engine's message, which names only what the
request named.
`type` stays about:blank, as the HTTP runtime writes it for every problem,
so `code` is what a client branches on. A BehaviorError, a defect in a
behavior's code, is not an EngineError: it answers 500 like any failure.
*/

import { HttpProblem } from '@superschematic/http-runtime';

import {
  BehaviorVetoError,
  CreateParamsError,
  CursorExpiredError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  PreconditionsError,
  SchemaDocumentError,
  UniqueConflictError,
  type EngineErrorCode,
} from '../errors.js';

/** The status of each engine error code. */
export const ENGINE_ERROR_STATUS: Readonly<Record<EngineErrorCode, number>> = {
  invalid_argument: 400,
  forbidden: 403,
  not_found: 404,
  unknown_namespace: 404,
  namespace_archived: 409,
  cursor_expired: 410,
  conflict: 409,
  name_taken: 409,
  incompatible_change: 409,
  vetoed: 409,
  seq_mismatch: 412,
  invalid_schema: 422,
  invalid_instance: 422,
  unavailable: 503,
};

/**
 * engineProblem returns the problem an engine error answers with, or
 * undefined for anything else.
 */
export function engineProblem(error: unknown): HttpProblem | undefined {
  if (error instanceof EngineError) {
    return new HttpProblem(ENGINE_ERROR_STATUS[error.code] ?? 500, error.message, {
      code: error.code,
      cause: error,
      ...detailsOf(error),
    });
  }
  return undefined;
}

function detailsOf(error: EngineError): { details?: unknown } {
  if (
    error instanceof SchemaDocumentError ||
    error instanceof InstanceValidationError ||
    error instanceof OperationParamsError ||
    error instanceof CreateParamsError ||
    error instanceof PreconditionsError
  ) {
    return { details: { issues: error.issues } };
  }
  if (error instanceof BehaviorVetoError) {
    return {
      details: {
        behavior: error.behavior,
        action: error.action,
        reason: error.reason,
        ...(error.vetoCode !== undefined ? { code: error.vetoCode } : {}),
        ...(error.vetoDetails !== undefined ? { details: error.vetoDetails } : {}),
      },
    };
  }
  if (error instanceof IncompatibleChangeError) {
    return { details: { changes: error.changes } };
  }
  if (error instanceof UniqueConflictError) {
    return { details: { fields: error.fields } };
  }
  if (error instanceof CursorExpiredError) {
    return { details: { after: error.after, floor: error.floor, head: error.head } };
  }
  return {};
}
