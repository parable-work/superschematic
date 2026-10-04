/*
How an engine refusal crosses HTTP. Each EngineError code has one status,
and the problem document carries the code as its `code` member; a refused
schema document, instance, operation's parameters or create's parameters
carries its issues as `details.issues`, a refused version its changes as `details.changes`, and
a behavior's veto the behavior, what it refused and why as `details`. The
detail is the engine's message, which names only what the request named.
`type` stays about:blank, as the HTTP runtime writes it for every problem,
so `code` is what a client branches on. A BehaviorError, a defect in a
behavior's code, is not an EngineError: it answers 500 like any failure.
*/

import { HttpProblem } from '@superschematic/http-runtime';

import {
  BehaviorVetoError,
  CreateParamsError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  SchemaDocumentError,
  type EngineErrorCode,
} from '../errors.js';

/** The status of each engine error code. */
export const ENGINE_ERROR_STATUS: Readonly<Record<EngineErrorCode, number>> = {
  invalid_argument: 400,
  forbidden: 403,
  not_found: 404,
  unknown_namespace: 404,
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
    error instanceof CreateParamsError
  ) {
    return { details: { issues: error.issues } };
  }
  if (error instanceof BehaviorVetoError) {
    return { details: { behavior: error.behavior, action: error.action, reason: error.reason } };
  }
  if (error instanceof IncompatibleChangeError) {
    return { details: { changes: error.changes } };
  }
  return {};
}
