/*
How an engine refusal crosses HTTP. Each EngineError code has one status,
and the problem document carries the code as its `code` member; a refused
schema document or instance carries its issues as `details.issues`, and a
refused version its changes as `details.changes`. The detail is the
engine's message, which names only what the request named. `type` stays
about:blank, as the HTTP runtime writes it for every problem, so `code`
is what a client branches on.
*/

import { HttpProblem, badRequest } from '@superschematic/http-runtime';

import { EngineError, IncompatibleChangeError, InstanceValidationError, SchemaDocumentError, type EngineErrorCode } from '../errors.js';

/** The status of each engine error code. */
export const ENGINE_ERROR_STATUS: Readonly<Record<EngineErrorCode, number>> = {
  invalid_argument: 400,
  forbidden: 403,
  not_found: 404,
  unknown_namespace: 404,
  conflict: 409,
  name_taken: 409,
  incompatible_change: 409,
  seq_mismatch: 412,
  invalid_schema: 422,
  invalid_instance: 422,
};

/**
 * engineProblem returns the problem an engine error answers with, or
 * undefined for anything else. A path whose percent-encoding does not
 * decode is a 400 too.
 */
export function engineProblem(error: unknown): HttpProblem | undefined {
  if (error instanceof EngineError) {
    return new HttpProblem(ENGINE_ERROR_STATUS[error.code] ?? 500, error.message, {
      code: error.code,
      cause: error,
      ...detailsOf(error),
    });
  }
  if (error instanceof URIError) {
    return badRequest('The request path is not valid percent-encoding', { cause: error });
  }
  return undefined;
}

function detailsOf(error: EngineError): { details?: unknown } {
  if (error instanceof SchemaDocumentError || error instanceof InstanceValidationError) {
    return { details: { issues: error.issues } };
  }
  if (error instanceof IncompatibleChangeError) {
    return { details: { changes: error.changes } };
  }
  return {};
}
